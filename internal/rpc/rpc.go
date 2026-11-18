// Package rpc implements the minimal LSP-framed Lithe backend protocol for
// a packaged Go plugin. It does not speak to the network or run arbitrary RPCs.
package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MaxFrameBytes = 64 << 20
const MaxHeaderBytes = 8192

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }
func Failed(code, message string) error {
	return &RPCError{Code: -32000, Message: message, Data: map[string]string{"code": code}}
}

type Ref struct {
	ContextID  string `json:"contextID"`
	Generation int    `json:"generation"`
}
type Invocation struct {
	FunctionID  string          `json:"functionID"`
	OperationID string          `json:"operationID"`
	Context     *Ref            `json:"context"`
	Params      json.RawMessage `json:"params"`
}
type Handler func(context.Context, Invocation, *Server) (any, error)

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type Server struct {
	r            *bufio.Reader
	w            io.Writer
	outputMu     sync.Mutex
	stateMu      sync.Mutex
	initialized  bool
	shuttingDown bool
	incoming     map[string]context.CancelFunc
	pending      map[string]chan frame
	nextID       atomic.Int64
	handlers     map[string]Handler
	onExit       func(runID string)
}

func New(r io.Reader, w io.Writer) *Server {
	return &Server{r: bufio.NewReader(r), w: w, incoming: make(map[string]context.CancelFunc), pending: make(map[string]chan frame), handlers: make(map[string]Handler)}
}
func (s *Server) Function(id string, h Handler) { s.handlers[id] = h }
func (s *Server) OnRunExit(h func(string))      { s.onExit = h }

func readFrame(r *bufio.Reader) (frame, error) {
	var m frame
	nhead := 0
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return m, err
		}
		nhead += len(line)
		if nhead > MaxHeaderBytes {
			return m, errors.New("header exceeds limit")
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		before, after, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(before), "Content-Length") {
			continue
		}
		if length != -1 {
			return m, errors.New("duplicate Content-Length")
		}
		after = strings.TrimSpace(after)
		if after == "" {
			return m, errors.New("empty Content-Length")
		}
		for _, c := range after {
			if c < '0' || c > '9' {
				return m, errors.New("invalid Content-Length")
			}
		}
		parsed, err := strconv.Atoi(after)
		if err != nil || parsed > MaxFrameBytes || parsed < 0 {
			return m, errors.New("frame exceeds limit")
		}
		length = parsed
	}
	if length < 0 {
		return m, errors.New("missing Content-Length")
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(r, raw); err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	return m, nil
}
func writeFrame(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	header := []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data)))
	if _, err = w.Write(header); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
func (s *Server) send(v any) error {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	return writeFrame(s.w, v)
}
func (s *Server) reply(id json.RawMessage, value any, reqErr error) {
	if reqErr != nil {
		var e *RPCError
		if !errors.As(reqErr, &e) {
			e = &RPCError{Code: -32000, Message: reqErr.Error(), Data: map[string]string{"code": "tomcat.failed"}}
		}
		if len(e.Message) > 1024 {
			e.Message = e.Message[:1024]
		}
		_ = s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": e})
		return
	}
	if value == nil {
		value = json.RawMessage("null")
	}
	_ = s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": value})
}
func validObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	b := bytes.TrimSpace(raw)
	return len(b) > 1 && b[0] == '{' && b[len(b)-1] == '}'
}
func rawKey(v json.RawMessage) string { return string(bytes.TrimSpace(v)) }
func (s *Server) handle(m frame) {
	if m.Method == "" && len(m.ID) > 0 {
		key := rawKey(m.ID)
		s.stateMu.Lock()
		ch := s.pending[key]
		delete(s.pending, key)
		s.stateMu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
		return
	}
	if m.Method == "$/cancelRequest" {
		var p struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(m.Params, &p)
		s.stateMu.Lock()
		fn := s.incoming[rawKey(p.ID)]
		s.stateMu.Unlock()
		if fn != nil {
			fn()
		}
		return
	}
	if m.Method == "exit" {
		return
	}
	if m.Method == "lithe/host/run/didExit" {
		var p struct {
			RunID string `json:"runID"`
		}
		if json.Unmarshal(m.Params, &p) == nil && s.onExit != nil {
			s.onExit(p.RunID)
		}
		return
	}
	if len(m.ID) == 0 {
		return
	}
	switch m.Method {
	case "initialize":
		s.stateMu.Lock()
		ready := s.initialized || s.shuttingDown
		s.stateMu.Unlock()
		if ready {
			s.reply(m.ID, nil, &RPCError{Code: -32600, Message: "initialize already received"})
			return
		}
		var params struct {
			Capabilities struct {
				Experimental struct {
					Functions struct {
						Versions []int `json:"versions"`
					} `json:"litheFunctions"`
					Logging struct {
						Versions []int `json:"versions"`
					} `json:"litheLogging"`
				} `json:"experimental"`
			} `json:"capabilities"`
		}
		if err := json.Unmarshal(m.Params, &params); err != nil {
			s.reply(m.ID, nil, &RPCError{Code: -32602, Message: "invalid initialization"})
			return
		}
		offered := func(list []int) bool {
			for _, v := range list {
				if v == 0 {
					return true
				}
			}
			return false
		}
		if !offered(params.Capabilities.Experimental.Logging.Versions) || !offered(params.Capabilities.Experimental.Functions.Versions) {
			s.reply(m.ID, nil, &RPCError{Code: -32600, Message: "backend requires litheLogging 0 and litheFunctions 0"})
			return
		}
		funcs := make([]map[string]string, 0, len(s.handlers))
		for id := range s.handlers {
			funcs = append(funcs, map[string]string{"id": id})
		}
		// Order is deterministic for feature snapshots.
		for i := 0; i < len(funcs); i++ {
			for j := i + 1; j < len(funcs); j++ {
				if funcs[j]["id"] < funcs[i]["id"] {
					funcs[j], funcs[i] = funcs[i], funcs[j]
				}
			}
		}
		s.stateMu.Lock()
		s.initialized = true
		s.stateMu.Unlock()
		s.reply(m.ID, map[string]any{
			"serverInfo":   map[string]string{"name": "lithe-tomcat-backend", "version": "0.2.0"},
			"capabilities": map[string]any{"experimental": map[string]any{"litheLogging": map[string]int{"version": 0}, "litheFunctions": map[string]any{"version": 0, "functions": funcs}}},
		}, nil)
	case "shutdown":
		s.stateMu.Lock()
		s.shuttingDown = true
		for _, cancel := range s.incoming {
			cancel()
		}
		s.stateMu.Unlock()
		s.reply(m.ID, nil, nil)
	case "lithe/function/invoke":
		s.stateMu.Lock()
		ready := s.initialized && !s.shuttingDown
		s.stateMu.Unlock()
		if !ready {
			s.reply(m.ID, nil, &RPCError{Code: -32002, Message: "backend not initialized"})
			return
		}
		var inv Invocation
		if !validObject(m.Params) || json.Unmarshal(m.Params, &inv) != nil || inv.Context == nil || inv.Context.ContextID == "" || inv.Context.Generation < 1 || !validObject(inv.Params) {
			s.reply(m.ID, nil, &RPCError{Code: -32602, Message: "invalid function params"})
			return
		}
		handler := s.handlers[inv.FunctionID]
		if handler == nil {
			s.reply(m.ID, nil, &RPCError{Code: -32601, Message: "unknown function"})
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		key := rawKey(m.ID)
		s.stateMu.Lock()
		s.incoming[key] = cancel
		s.stateMu.Unlock()
		go func() {
			defer cancel()
			defer func() { s.stateMu.Lock(); delete(s.incoming, key); s.stateMu.Unlock() }()
			value, err := handler(ctx, inv, s)
			if ctx.Err() != nil {
				err = &RPCError{Code: -32800, Message: "request cancelled"}
			}
			s.reply(m.ID, value, err)
		}()
	default:
		s.reply(m.ID, nil, &RPCError{Code: -32601, Message: "unsupported method"})
	}
}

// Request calls a host service through the existing Lithe backend connection.
// It never sends the user's project data to any external service.
func (s *Server) Request(ctx context.Context, method string, params any, out any) error {
	s.stateMu.Lock()
	ready := s.initialized && !s.shuttingDown
	s.stateMu.Unlock()
	if !ready {
		return errors.New("backend not ready")
	}
	id := "tomcat-" + strconv.FormatInt(s.nextID.Add(1), 10)
	key := strconv.Quote(id)
	ch := make(chan frame, 1)
	s.stateMu.Lock()
	s.pending[key] = ch
	s.stateMu.Unlock()
	defer func() { s.stateMu.Lock(); delete(s.pending, key); s.stateMu.Unlock() }()
	if err := s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-bounded.Done():
		return bounded.Err()
	}
}

// Listen blocks until the host ends the connection. Each invocation is async
// so host replies can be processed while a handler awaits a host request.
func (s *Server) Listen() error {
	for {
		m, err := readFrame(s.r)
		if err != nil {
			s.stateMu.Lock()
			for _, cancel := range s.incoming {
				cancel()
			}
			s.stateMu.Unlock()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Method == "exit" {
			return nil
		}
		s.handle(m)
	}
}
