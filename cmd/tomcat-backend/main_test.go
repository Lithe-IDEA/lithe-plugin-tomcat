package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Lithe-IDEA/lithe-plugin-tomcat/internal/rpc"
	"github.com/Lithe-IDEA/lithe-plugin-tomcat/internal/tomcat"
)

// This is a protocol-level synthetic host, not a native Lithe UI or Tomcat
// installation test. It exercises the same LSP frames and host RPC contracts.
type syntheticHost struct {
	writer  *io.PipeWriter
	reader  *io.PipeReader
	mu      sync.Mutex
	pending map[int]chan map[string]json.RawMessage
	serial  int
	runPlan map[string]json.RawMessage
	stopID  string
	root    string
	state   string
}

func (h *syntheticHost) send(value any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(h.writer, "Content-Length: %d\r\n\r\n", len(b)); err != nil {
		return err
	}
	_, err = h.writer.Write(b)
	return err
}
func (h *syntheticHost) serve(done chan<- error) {
	r := bufio.NewReader(h.reader)
	for {
		length := -1
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				done <- err
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "Content-Length: ") {
				length, _ = strconv.Atoi(strings.TrimPrefix(line, "Content-Length: "))
			}
		}
		if length < 0 || length > 4<<20 {
			done <- fmt.Errorf("invalid frame size %d", length)
			return
		}
		b := make([]byte, length)
		if _, err := io.ReadFull(r, b); err != nil {
			done <- err
			return
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			done <- err
			return
		}
		var id int
		_ = json.Unmarshal(m["id"], &id)
		if len(m["method"]) == 0 {
			h.mu.Lock()
			ch := h.pending[id]
			delete(h.pending, id)
			h.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		var method string
		_ = json.Unmarshal(m["method"], &method)
		var result any
		switch method {
		case "lithe/host/getCapabilities":
			result = map[string]any{"services": map[string]any{"workspace": map[string]any{"methods": []string{"lithe/host/workspace/getContext"}}, "run": map[string]any{"methods": []string{"lithe/host/run/start", "lithe/host/run/stop"}}}}
		case "lithe/host/workspace/getContext":
			result = map[string]any{"contextID": "ctx-one", "generation": 1, "stateDirectory": h.state, "authority": map[string]string{"kind": "local"}, "folders": []any{map[string]string{"path": h.root, "rootID": "r1"}}}
		case "lithe/host/run/start":
			var p map[string]json.RawMessage
			_ = json.Unmarshal(m["params"], &p)
			h.mu.Lock()
			h.runPlan = p
			h.mu.Unlock()
			result = map[string]string{"runID": "run-test-1"}
		case "lithe/host/run/stop":
			var p map[string]string
			_ = json.Unmarshal(m["params"], &p)
			h.mu.Lock()
			h.stopID = p["runID"]
			h.mu.Unlock()
			result = nil
		default:
			done <- fmt.Errorf("unexpected host method: %s", method)
			return
		}
		if err := h.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(m["id"]), "result": result}); err != nil {
			done <- err
			return
		}
	}
}
func (h *syntheticHost) call(t *testing.T, method string, params any) (map[string]json.RawMessage, error) {
	t.Helper()
	h.mu.Lock()
	h.serial++
	id := h.serial
	ch := make(chan map[string]json.RawMessage, 1)
	h.pending[id] = ch
	h.mu.Unlock()
	if err := h.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case got := <-ch:
		if len(got["error"]) > 0 {
			return got, fmt.Errorf("%s", got["error"])
		}
		return got, nil
	case <-time.After(7 * time.Second):
		return nil, fmt.Errorf("timed out calling %s", method)
	}
}
func (h *syntheticHost) invoke(t *testing.T, fn string, params any) (map[string]json.RawMessage, error) {
	return h.call(t, "lithe/function/invoke", map[string]any{"functionID": fn, "operationID": "op-test-1", "context": map[string]any{"contextID": "ctx-one", "generation": 1}, "params": params})
}
func makeWAR(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, err := z.Create("WEB-INF/web.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("<web-app/>")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
func fakeTomcat(t *testing.T) (tomcat.Config, string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "Tomcat with space")
	work := filepath.Join(root, "Example project")
	for _, dir := range []string{"bin", "lib", "conf", "webapps", "temp"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"bin/bootstrap.jar", "bin/tomcat-juli.jar", "lib/catalina.jar", "conf/server.xml", "conf/catalina.properties"} {
		if err := os.WriteFile(filepath.Join(home, filepath.FromSlash(p)), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(work, "target"), 0755); err != nil {
		t.Fatal(err)
	}
	makeWAR(t, filepath.Join(work, "target", "app.war"))
	return tomcat.Config{Home: home, Context: "/app", WAR: "target/app.war", DebugPort: 5566}, work, home
}
func TestBackendWorkspaceRunDeployProtocol(t *testing.T) {
	config, root, home := fakeTomcat(t)
	toBackendR, toBackendW := io.Pipe()
	fromBackendR, fromBackendW := io.Pipe()
	defer toBackendR.Close()
	defer toBackendW.Close()
	defer fromBackendR.Close()
	defer fromBackendW.Close()
	h := &syntheticHost{reader: fromBackendR, writer: toBackendW, pending: map[int]chan map[string]json.RawMessage{}, root: root, state: filepath.Join(t.TempDir(), "private")}
	backend := rpc.New(toBackendR, fromBackendW)
	s := newSession()
	backend.OnRunExit(s.onRunExit)
	for _, fn := range []string{"tomcat.get", "tomcat.save", "tomcat.inspect", "tomcat.discover", "tomcat.planDeploy", "tomcat.deploy", "tomcat.start", "tomcat.stop"} {
		backend.Function(fn, s.handle)
	}
	go func() { _ = backend.Listen() }()
	done := make(chan error, 1)
	go h.serve(done)
	if _, err := h.call(t, "initialize", map[string]any{"capabilities": map[string]any{"experimental": map[string]any{"litheFunctions": map[string]any{"versions": []int{0}}, "litheLogging": map[string]any{"versions": []int{0}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.invoke(t, "tomcat.save", map[string]any{"config": config}); err != nil {
		t.Fatal(err)
	}
	if result, err := h.invoke(t, "tomcat.inspect", map[string]any{}); err != nil || !bytes.Contains(result["result"], []byte("webapps")) {
		t.Fatalf("inspect: %v %s", err, result["result"])
	}
	planMsg, err := h.invoke(t, "tomcat.planDeploy", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var plan tomcat.DeployPlan
	if err := json.Unmarshal(planMsg["result"], &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Action != "create" || plan.Token == "" {
		t.Fatalf("invalid plan: %+v", plan)
	}
	if _, err := os.Stat(plan.Target); !os.IsNotExist(err) {
		t.Fatal("preview wrote target")
	}
	if _, err := h.invoke(t, "tomcat.deploy", map[string]any{"confirmationToken": "!" + plan.Token[1:]}); err == nil {
		t.Fatal("accepted unconfirmed deployment")
	}
	if _, err := h.invoke(t, "tomcat.deploy", map[string]any{"confirmationToken": plan.Token}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(plan.Target); err != nil || st.Size() == 0 {
		t.Fatal("deployed WAR missing", err)
	}
	started, err := h.invoke(t, "tomcat.start", map[string]any{"debug": true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(started["result"], []byte("run-test-1")) {
		t.Fatal("no run id", started)
	}
	h.mu.Lock()
	rp := h.runPlan
	h.mu.Unlock()
	if len(rp) == 0 || !bytes.Contains(rp["process"], []byte("127.0.0.1:5566")) || !bytes.Contains(rp["process"], []byte(`"tool":"java"`)) || !bytes.Contains(rp["debug"], []byte(`"configurationType":"java"`)) || !bytes.Contains(rp["debug"], []byte(`"kind":"connection"`)) {
		t.Fatalf("bad host run/start plan: %s", rp["process"])
	}
	if _, err := h.invoke(t, "tomcat.start", map[string]any{"debug": false}); err == nil {
		t.Fatal("duplicate start was allowed")
	}
	if _, err := h.invoke(t, "tomcat.stop", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	stopped := h.stopID
	h.mu.Unlock()
	if stopped != "run-test-1" {
		t.Fatalf("wrong stopped run: %s", stopped)
	}
	if err := h.send(map[string]any{"jsonrpc": "2.0", "method": "lithe/host/run/didExit", "params": map[string]any{"runID": "run-test-1", "exitCode": 0}}); err != nil {
		t.Fatal(err)
	}
	// Wait for the notification to be applied rather than assuming synchronous delivery.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.runID("ctx-one:1") == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.runID("ctx-one:1") != "" {
		t.Fatal("run exit did not clear session")
	}
	if _, err := h.invoke(t, "tomcat.save", map[string]any{"config": config}); err != nil {
		t.Fatal("save after exit", err)
	}
	if saved, err := tomcat.LoadConfig(h.state); err != nil || saved.Home != home {
		t.Fatal("workspace state not saved", saved, err)
	}
	if _, err := h.call(t, "shutdown", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := h.send(map[string]any{"jsonrpc": "2.0", "method": "exit"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil && err != io.EOF && err != io.ErrClosedPipe {
			t.Logf("synthetic host closing: %v", err)
		}
	default:
	}
}
func TestRunSessionFastExitAndGeneration(t *testing.T) {
	s := newSession()
	if !s.reserve("ctx:1") {
		t.Fatal("cannot reserve")
	}
	s.onRunExit("short-run")
	s.finishStart("ctx:1", "short-run")
	if s.runID("ctx:1") != "" {
		t.Fatal("fast exit recorded as running")
	}
	if !s.reserve("ctx:2") {
		t.Fatal("new generation should have independent run")
	}
	s.finishStart("ctx:2", "new-run")
	if s.runID("ctx:2") != "new-run" {
		t.Fatal("new generation lost")
	}
	s.onRunExit("new-run")
	if s.runID("ctx:2") != "" {
		t.Fatal("didExit did not clear new generation")
	}
}
func TestRunPlanJavaArgsFixture(t *testing.T) {
	if _, err := exec.LookPath("javac"); err != nil {
		t.Skip("javac not installed")
	}
	if _, err := exec.LookPath("jar"); err != nil {
		t.Skip("jar not installed")
	}
	if _, err := exec.LookPath("java"); err != nil {
		t.Skip("java not installed")
	}
	c, workspace, home := fakeTomcat(t)
	srcDir := filepath.Join(t.TempDir(), "source", "org", "apache", "catalina", "startup")
	classesDir := filepath.Join(t.TempDir(), "classes")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(classesDir, 0755); err != nil {
		t.Fatal(err)
	}
	source := `package org.apache.catalina.startup;
public final class Bootstrap {
 public static void main(String[] args) {
  System.out.println("START_OK HOME="+System.getProperty("catalina.home") + " BASE="+System.getProperty("catalina.base")+" ACTION="+args[0]);
 }
}`
	src := filepath.Join(srcDir, "Bootstrap.java")
	if err := os.WriteFile(src, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("javac", "-d", classesDir, src).CombinedOutput(); err != nil {
		t.Fatalf("javac %s: %s", err, out)
	}
	jarPath := filepath.Join(home, "bin", "bootstrap.jar")
	if out, err := exec.Command("jar", "--create", "--file", jarPath, "-C", classesDir, ".").CombinedOutput(); err != nil {
		t.Fatalf("jar %s: %s", err, out)
	}
	// Do not collide with anyone else's local JDWP listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c.DebugPort = ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	checked, err := tomcat.Check(c, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, debug := range []bool{false, true} {
		plan := checked.Startup(debug)
		command := exec.Command("java", plan.Arguments...)
		command.Dir = workspace
		command.Env = append(os.Environ(), "CATALINA_HOME="+home, "CATALINA_BASE="+home)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("debug=%t java failed: %v\n%s", debug, err, out)
		}
		if !bytes.Contains(out, []byte("START_OK HOME="+home)) || !bytes.Contains(out, []byte("ACTION=start")) {
			t.Fatalf("debug=%t java argv boundary mismatch: %s", debug, out)
		}
	}
}
