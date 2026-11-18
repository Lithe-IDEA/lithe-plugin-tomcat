package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Lithe-IDEA/lithe-plugin-tomcat/internal/rpc"
	"github.com/Lithe-IDEA/lithe-plugin-tomcat/internal/tomcat"
)

type hostContext struct {
	ContextID      string `json:"contextID"`
	Generation     int    `json:"generation"`
	StateDirectory string `json:"stateDirectory"`
	Authority      struct {
		Kind string `json:"kind"`
	} `json:"authority"`
	Folders []struct {
		Path   string `json:"path"`
		RootID string `json:"rootID"`
	} `json:"folders"`
}

type session struct {
	mu              sync.Mutex
	runs            map[string]string // context ID + generation -> owned run ID
	starting        map[string]bool
	stopping        map[string]bool
	deploying       map[string]bool
	exited          map[string]struct{} // early host exit reported before start ack
	approved        map[string]deployApproval
	watches         map[string]context.CancelFunc
	watching        map[string]bool
	watchError      map[string]string
	watchGeneration map[string]uint64
}

func newSession() *session {
	return &session{runs: make(map[string]string), starting: make(map[string]bool), stopping: make(map[string]bool), deploying: make(map[string]bool), exited: make(map[string]struct{}), approved: make(map[string]deployApproval), watches: make(map[string]context.CancelFunc), watching: make(map[string]bool), watchError: make(map[string]string), watchGeneration: make(map[string]uint64)}
}
func (s *session) onRunExit(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runID == "" {
		return
	}
	found := false
	for ctx, id := range s.runs {
		if id == runID {
			found = true
			delete(s.runs, ctx)
			delete(s.stopping, ctx)
			if cancel := s.watches[ctx]; cancel != nil {
				cancel()
			}
			s.watchGeneration[ctx]++
			delete(s.watches, ctx)
			delete(s.watching, ctx)
		}
	}
	if !found && len(s.starting) > 0 && len(s.exited) < 128 {
		s.exited[runID] = struct{}{}
	}
}
func (s *session) runID(contextID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[contextID]
}
func (s *session) reserve(contextID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starting[contextID] || s.deploying[contextID] || s.runs[contextID] != "" {
		return false
	}
	s.starting[contextID] = true
	return true
}
func (s *session) finishStart(contextID, runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.starting, contextID)
	if runID != "" {
		if _, alreadyExited := s.exited[runID]; alreadyExited {
			delete(s.exited, runID)
		} else {
			s.runs[contextID] = runID
		}
	}
}
func (s *session) reserveStop(contextID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.runs[contextID]
	if id == "" || s.stopping[contextID] {
		return "", false
	}
	s.stopping[contextID] = true
	return id, true
}
func (s *session) busy(contextID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starting[contextID] || s.deploying[contextID] || s.runs[contextID] != ""
}
func (s *session) reserveDeploy(contextID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starting[contextID] || s.deploying[contextID] || s.runs[contextID] != "" {
		return false
	}
	s.deploying[contextID] = true
	return true
}
func (s *session) finishDeploy(contextID string) {
	s.mu.Lock()
	delete(s.deploying, contextID)
	s.mu.Unlock()
}
func (s *session) stopFailed(contextID string) {
	s.mu.Lock()
	delete(s.stopping, contextID)
	s.mu.Unlock()
}

func (s *session) getContext(ctx context.Context, call rpc.Invocation, conn *rpc.Server) (hostContext, string, error) {
	var h hostContext
	if call.Context == nil {
		return h, "", rpc.Failed("tomcat.contextMissing", "No workspace is active")
	}
	var caps struct {
		Services map[string]struct {
			Methods []string `json:"methods"`
		} `json:"services"`
	}
	if err := conn.Request(ctx, "lithe/host/getCapabilities", map[string]any{}, &caps); err != nil {
		return h, "", rpc.Failed("tomcat.hostUnavailable", err.Error())
	}
	supported := false
	for _, v := range caps.Services {
		for _, m := range v.Methods {
			if m == "lithe/host/workspace/getContext" {
				supported = true
			}
		}
	}
	if !supported {
		return h, "", rpc.Failed("tomcat.hostUnavailable", "Host does not expose workspace metadata")
	}
	if err := conn.Request(ctx, "lithe/host/workspace/getContext", map[string]any{"context": call.Context}, &h); err != nil {
		return h, "", rpc.Failed("tomcat.contextUnavailable", err.Error())
	}
	if h.ContextID != call.Context.ContextID || h.Generation != call.Context.Generation || h.Authority.Kind != "local" || len(h.Folders) != 1 || h.Folders[0].Path == "" || h.StateDirectory == "" {
		return h, "", rpc.Failed("tomcat.workspaceUnsupported", "Tomcat requires exactly one local workspace root")
	}
	return h, h.Folders[0].Path, nil
}
func (s *session) handle(ctx context.Context, call rpc.Invocation, conn *rpc.Server) (any, error) {
	h, root, err := s.getContext(ctx, call, conn)
	if err != nil {
		return nil, err
	}
	loaded, err := tomcat.LoadConfig(h.StateDirectory)
	if err != nil {
		return nil, rpc.Failed("tomcat.configReadFailed", err.Error())
	}
	runKey := h.ContextID + ":" + strconv.Itoa(h.Generation)
	getRunID := func() string { return s.runID(runKey) }
	switch call.FunctionID {
	case "tomcat.get":
		watching, reason := s.watchStatus(runKey)
		return map[string]any{"config": loaded, "running": getRunID() != "", "runID": getRunID(), "workspaceRoot": root, "watching": watching, "watchError": reason}, nil
	case "tomcat.discover":
		modules, err := tomcat.DiscoverWebModules(root)
		if err != nil {
			return nil, rpc.Failed("tomcat.discoveryFailed", err.Error())
		}
		return map[string]any{"modules": modules}, nil
	case "tomcat.save":
		if s.busy(runKey) {
			return nil, rpc.Failed("tomcat.running", "Wait for Tomcat or deployment to finish before editing configuration")
		}
		var args struct {
			Config tomcat.Config `json:"config"`
		}
		if err := json.Unmarshal(call.Params, &args); err != nil {
			return nil, rpc.Failed("tomcat.invalidConfig", "Invalid config")
		}
		if err := tomcat.SaveConfig(h.StateDirectory, args.Config); err != nil {
			return nil, rpc.Failed("tomcat.invalidConfig", err.Error())
		}
		return map[string]any{"saved": true}, nil
	case "tomcat.inspect":
		checked, err := tomcat.Check(loaded, root, false)
		if err != nil {
			return nil, rpc.Failed("tomcat.installInvalid", err.Error())
		}
		return map[string]any{"home": checked.HomePath, "base": checked.BasePath, "webapps": checked.Webapps, "war": checked.WARPath, "exploded": checked.ExplodedPath, "artifact": checked.Artifact, "context": checked.Context, "debugPort": checked.DebugPort, "server": checked.Server}, nil
	case "tomcat.planDeploy", "tomcat.deploy":
		checked, err := tomcat.Check(loaded, root, true)
		if err != nil {
			return nil, rpc.Failed("tomcat.installInvalid", err.Error())
		}
		if !s.canDeploy(runKey, loaded, checked) {
			return nil, rpc.Failed("tomcat.running", "Stop Tomcat before deploying; live WAR deploy requires opt-in and Tomcat autoDeploy=true")
		}
		if call.FunctionID == "tomcat.planDeploy" {
			plan, err := tomcat.InspectDeploy(checked)
			if err != nil {
				return nil, rpc.Failed("tomcat.deployInvalid", err.Error())
			}
			return plan, nil
		}
		if !s.reserveDeployFor(runKey, loaded, checked) {
			return nil, rpc.Failed("tomcat.deployBusy", "Tomcat started or another deployment is active")
		}
		defer s.finishDeploy(runKey)
		var args struct {
			Token string `json:"confirmationToken"`
		}
		if err := json.Unmarshal(call.Params, &args); err != nil || len(args.Token) != 64 {
			return nil, rpc.Failed("tomcat.confirmationRequired", "First inspect deployment and explicitly confirm its preview")
		}
		plan, err := tomcat.Deploy(checked, args.Token)
		if err != nil {
			return nil, rpc.Failed("tomcat.deployFailed", err.Error())
		}
		s.approval(runKey, plan)
		return map[string]any{"deployed": true, "target": plan.Target, "sha256": plan.SourceHash, "action": plan.Action}, nil
	case "tomcat.start":
		var args struct {
			Debug bool `json:"debug"`
		}
		if err := json.Unmarshal(call.Params, &args); err != nil {
			return nil, rpc.Failed("tomcat.invalidParams", err.Error())
		}
		if !s.reserve(runKey) {
			return nil, rpc.Failed("tomcat.alreadyRunning", "Tomcat is already running or starting")
		}
		runID := ""
		defer func() { s.finishStart(runKey, runID) }()
		checked, err := tomcat.Check(loaded, root, false)
		if err != nil {
			return nil, rpc.Failed("tomcat.installInvalid", err.Error())
		}
		if args.Debug {
			address := "127.0.0.1:" + strconv.Itoa(checked.DebugPort)
			conn, err := net.DialTimeout("tcp", address, 150*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil, rpc.Failed("tomcat.debugPortBusy", "JDWP port is already in use")
			}
		}
		var caps struct {
			Services map[string]struct {
				Methods []string `json:"methods"`
			} `json:"services"`
		}
		if err := conn.Request(ctx, "lithe/host/getCapabilities", map[string]any{}, &caps); err != nil {
			return nil, rpc.Failed("tomcat.hostUnavailable", err.Error())
		}
		supported := false
		for _, v := range caps.Services {
			for _, m := range v.Methods {
				if m == "lithe/host/run/start" {
					supported = true
				}
			}
		}
		if !supported {
			return nil, rpc.Failed("tomcat.runUnavailable", "Host does not provide run/start")
		}
		title := "Tomcat"
		if args.Debug {
			title = "Tomcat (JDWP " + strconv.Itoa(checked.DebugPort) + ")"
		}
		var resp struct {
			RunID string `json:"runID"`
		}
		// Once submitted to host, complete the response rather than cancelling
		// mid-start; host owns this Run session and its Stop button.
		startParams := map[string]any{"context": call.Context, "label": title, "process": checked.Startup(args.Debug)}
		if args.Debug {
			// The java alias is a public debug-adapter configuration type, not
			// a Tomcat-owned adapter. Lithe's Run session waits for this JDWP
			// listener, then attaches the Java Support adapter if installed.
			startParams["debug"] = map[string]any{"configurationType": "java", "target": map[string]any{"kind": "connection", "port": checked.DebugPort}}
		}
		err = conn.Request(context.Background(), "lithe/host/run/start", startParams, &resp)
		if err != nil {
			return nil, rpc.Failed("tomcat.startFailed", err.Error())
		}
		if resp.RunID == "" {
			return nil, rpc.Failed("tomcat.startFailed", "Host returned no run ID")
		}
		runID = resp.RunID
		// finishStart runs as this handler returns. The watcher validates the
		// acknowledged run ID after the host owns the process.
		if loaded.WatchChanges {
			go func() {
				time.Sleep(50 * time.Millisecond)
				if reason := s.startWatch(runKey, runID, loaded, root); reason != "" {
					s.warnWatch(runKey, reason)
				}
			}()
		}
		return map[string]any{"runID": runID, "debug": args.Debug, "debugPort": checked.DebugPort}, nil
	case "tomcat.stop":
		id, ok := s.reserveStop(runKey)
		if !ok {
			return nil, rpc.Failed("tomcat.notRunning", "No running Tomcat session to stop")
		}
		s.cancelWatch(runKey)
		if err := conn.Request(context.Background(), "lithe/host/run/stop", map[string]string{"runID": id}, nil); err != nil {
			s.stopFailed(runKey)
			return nil, rpc.Failed("tomcat.stopFailed", err.Error())
		}
		return map[string]any{"stopRequested": true, "runID": id}, nil
	default:
		return nil, rpc.Failed("tomcat.unknownFunction", "Unknown function")
	}
}
func main() {
	// The host owns stdout: it must contain only Content-Length JSON-RPC frames.
	s := newSession()
	backend := rpc.New(os.Stdin, os.Stdout)
	backend.OnRunExit(s.onRunExit)
	for _, id := range []string{"tomcat.get", "tomcat.save", "tomcat.inspect", "tomcat.discover", "tomcat.planDeploy", "tomcat.deploy", "tomcat.start", "tomcat.stop"} {
		backend.Function(id, s.handle)
	}
	if err := backend.Listen(); err != nil && !errors.Is(err, context.Canceled) {
		// Do not print paths, workspace data, or the JSON-RPC stream to stderr.
		os.Exit(1)
	}
}
