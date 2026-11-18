package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFrameWithUnicodeAndMultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	values := []any{map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]string{"label": "中文 Tomcat"}}, map[string]any{"jsonrpc": "2.0", "method": "initialized"}}
	for _, v := range values {
		if err := writeFrame(&buf, v); err != nil {
			t.Fatal(err)
		}
	}
	r := bufio.NewReader(&buf)
	one, err := readFrame(r)
	if err != nil || one.Method != "initialize" {
		t.Fatal(one, err)
	}
	if !bytes.Contains(one.Params, []byte("中文 Tomcat")) {
		t.Fatal("unicode damaged")
	}
	two, err := readFrame(r)
	if err != nil || two.Method != "initialized" {
		t.Fatal(two, err)
	}
}
func TestFrameRejectsInvalidHeaders(t *testing.T) {
	for _, raw := range []string{"Content-Length: -1\r\n\r\n", "Content-Length: 9999999999\r\n\r\n", "Content-Type: text/plain\r\n\r\n", "Content-Length: 10\r\nContent-Length: 20\r\n\r\n", strings.Repeat("X", MaxHeaderBytes) + "\r\n\r\n"} {
		if _, err := readFrame(bufio.NewReader(strings.NewReader(raw))); err == nil {
			t.Fatalf("accepted bad frame header: %q", raw[:min(40, len(raw))])
		}
	}
}
func rpcPair(t *testing.T) (*Server, *bufio.Reader, io.WriteCloser, func()) {
	t.Helper()
	hostToBackend, hostWriter := io.Pipe()
	hostReader, backendWriter := io.Pipe()
	server := New(hostToBackend, backendWriter)
	stop := func() { hostWriter.Close(); backendWriter.Close(); hostToBackend.Close(); hostReader.Close() }
	return server, bufio.NewReader(hostReader), hostWriter, stop
}
func sendHost(t *testing.T, w io.Writer, obj any) {
	t.Helper()
	if err := writeFrame(w, obj); err != nil {
		t.Fatal(err)
	}
}
func initHost(t *testing.T, server *Server, reader *bufio.Reader, writer io.Writer) {
	t.Helper()
	sendHost(t, writer, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"capabilities": map[string]any{"experimental": map[string]any{"litheLogging": map[string]any{"versions": []int{0}}, "litheFunctions": map[string]any{"versions": []int{0}}}}}})
	frame, err := readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Error != nil {
		t.Fatal(frame.Error)
	}
	if !bytes.Contains(frame.Result, []byte("litheFunctions")) {
		t.Fatal("not negotiated")
	}
}
func TestFunctionHostRequestAndCancellation(t *testing.T) {
	server, reader, writer, stop := rpcPair(t)
	defer stop()
	server.Function("tomcat.probe", func(ctx context.Context, call Invocation, server *Server) (any, error) {
		var reply struct {
			Value string `json:"value"`
		}
		err := server.Request(ctx, "lithe/host/getCapabilities", map[string]any{}, &reply)
		if err != nil {
			return nil, err
		}
		return map[string]string{"echo": reply.Value}, nil
	})
	server.Function("tomcat.cancel", func(ctx context.Context, call Invocation, server *Server) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	done := make(chan error, 1)
	go func() { done <- server.Listen() }()
	initHost(t, server, reader, writer)
	call := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "lithe/function/invoke", "params": map[string]any{"functionID": "tomcat.probe", "operationID": "call-1", "context": map[string]any{"contextID": "workspace-1", "generation": 1}, "params": map[string]any{}}}
	sendHost(t, writer, call)
	requested, err := readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if requested.Method != "lithe/host/getCapabilities" {
		t.Fatalf("expected host request: %#v", requested)
	}
	sendHost(t, writer, map[string]any{"jsonrpc": "2.0", "id": requested.ID, "result": map[string]string{"value": "host-ready"}})
	response, err := readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	if !bytes.Contains(response.Result, []byte("host-ready")) {
		t.Fatalf("unexpected response: %s", response.Result)
	}
	sendHost(t, writer, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "lithe/function/invoke", "params": map[string]any{"functionID": "tomcat.cancel", "operationID": "call-2", "context": map[string]any{"contextID": "workspace-1", "generation": 1}, "params": map[string]any{}}})
	sendHost(t, writer, map[string]any{"jsonrpc": "2.0", "method": "$/cancelRequest", "params": map[string]any{"id": 3}})
	// The reader serializes notifications, but a cancel may arrive before an
	// asynchronous function has started: both must still be cancelled.
	response, err = readFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32800 {
		t.Fatalf("expected cancellation: %#v", response)
	}
	sendHost(t, writer, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "shutdown"})
	last, err := readFrame(reader)
	if err != nil || last.Error != nil {
		t.Fatal(last, err)
	}
	stop()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend failed to exit")
	}
}
func TestInitializationRequiresLogging(t *testing.T) {
	server, reader, writer, stop := rpcPair(t)
	defer stop()
	go server.Listen()
	sendHost(t, writer, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"capabilities": map[string]any{"experimental": map[string]any{"litheFunctions": map[string]any{"versions": []int{0}}}}}})
	msg, err := readFrame(reader)
	if err != nil || msg.Error == nil {
		t.Fatal(msg, err)
	}
	b, _ := json.Marshal(msg.Error)
	if !bytes.Contains(b, []byte("litheLogging")) {
		t.Fatal(string(b))
	}
}
