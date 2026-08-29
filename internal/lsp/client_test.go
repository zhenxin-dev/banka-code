package lspclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

type recordingWriteCloser struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	closed bool
}

func (w *recordingWriteCloser) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	return w.buffer.Write(value)
}

func (w *recordingWriteCloser) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	return nil
}

func (w *recordingWriteCloser) messages(t *testing.T) []map[string]any {
	t.Helper()
	w.mu.Lock()
	data := append([]byte(nil), w.buffer.Bytes()...)
	w.mu.Unlock()
	reader := bufio.NewReader(bytes.NewReader(data))
	var result []map[string]any
	for {
		body, err := readFrame(reader)
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode response frame: %v", err)
		}
		var message map[string]any
		if err := json.Unmarshal(body, &message); err != nil {
			t.Fatalf("decode response JSON: %v", err)
		}
		result = append(result, message)
	}
	return result
}

func TestHandleServerRequestUsesNullForHeadlessAndRefresh(t *testing.T) {
	methods := []string{
		"window/showMessageRequest",
		"window/workDoneProgress/create",
		"client/registerCapability",
		"client/unregisterCapability",
		"workspace/semanticTokens/refresh",
		"workspace/inlayHint/refresh",
		"workspace/codeLens/refresh",
		"workspace/codeAction/refresh",
		"workspace/diagnostic/refresh",
		"workspace/diagnostics/refresh",
		"workspace/inlineValue/refresh",
		"workspace/foldingRange/refresh",
	}
	writer := &recordingWriteCloser{}
	client := &Client{stdin: writer, done: make(chan struct{}), dynamicCaps: make(map[string]dynamicCapability)}
	for index, method := range methods {
		params := json.RawMessage(`{}`)
		if method == "window/showMessageRequest" {
			params = json.RawMessage(`{"actions":[{"title":"dangerous default"}]}`)
		}
		client.handleServerRequest(json.RawMessage(strconv.Itoa(index+1)), method, params)
	}
	messages := writer.messages(t)
	if len(messages) != len(methods) {
		t.Fatalf("got %d responses, want %d", len(messages), len(methods))
	}
	for index, message := range messages {
		if _, hasError := message["error"]; hasError {
			t.Fatalf("method %s returned an error: %#v", methods[index], message)
		}
		if result, present := message["result"]; !present || result != nil {
			t.Fatalf("method %s result = %#v, want JSON null", methods[index], result)
		}
	}
}

func TestDynamicCapabilityRegistrationIsReflectedAndRemoved(t *testing.T) {
	client := &Client{stdin: &recordingWriteCloser{}, done: make(chan struct{}), dynamicCaps: make(map[string]dynamicCapability)}
	client.handleServerRequest(json.RawMessage("1"), "client/registerCapability", json.RawMessage(`{
		"registrations":[{"id":"diag","method":"textDocument/diagnostic","registerOptions":{"identifier":"demo"}}]
	}`))
	caps := client.Capabilities()
	provider, ok := caps["diagnosticProvider"].(map[string]any)
	if !ok || provider["identifier"] != "demo" {
		t.Fatalf("dynamic diagnostic capability missing: %#v", caps)
	}
	client.handleServerRequest(json.RawMessage("2"), "client/unregisterCapability", json.RawMessage(`{"unregistrations":[{"id":"diag"}]}`))
	if _, ok := client.Capabilities()["diagnosticProvider"]; ok {
		t.Fatalf("dynamic diagnostic capability remained after unregister: %#v", client.Capabilities())
	}
}

func TestActivityHandlerIsCalledWhenExchangeSettles(t *testing.T) {
	client := &Client{}
	var calls atomic.Int32
	client.SetActivityHandler(func() { calls.Add(1) })
	finish := client.beginActivity()
	if got := client.activeRequests(); got != 1 {
		t.Fatalf("active requests while running = %d, want 1", got)
	}
	finish()
	if got := client.activeRequests(); got != 0 {
		t.Fatalf("active requests after finish = %d, want 0", got)
	}
	if calls.Load() < 2 {
		t.Fatalf("activity callback calls = %d, want start and settle callbacks", calls.Load())
	}
}

func TestFinishRecordsExitErrorAfterReaderEOF(t *testing.T) {
	client := &Client{done: make(chan struct{}), pending: make(map[string]chan rpcResponse)}
	client.finish(io.EOF)

	exitErr := &exec.ExitError{}
	client.finish(exitErr)

	client.stateMu.RLock()
	got := client.processErr
	client.stateMu.RUnlock()
	if got != exitErr {
		t.Fatalf("process error = %#v, want later exit error %#v", got, exitErr)
	}
}

func TestFinishPrefersExitErrorOverPipeError(t *testing.T) {
	client := &Client{done: make(chan struct{}), pending: make(map[string]chan rpcResponse)}
	client.finish(io.ErrClosedPipe)

	exitErr := &exec.ExitError{}
	client.finish(exitErr)

	client.stateMu.RLock()
	got := client.processErr
	client.stateMu.RUnlock()
	if got != exitErr {
		t.Fatalf("process error = %#v, want exit error %#v", got, exitErr)
	}
}

func TestLanguageIDForCommonExtensionlessAndReactFiles(t *testing.T) {
	tests := map[string]string{
		"Dockerfile":     "dockerfile",
		"Makefile":       "makefile",
		"justfile":       "just",
		"CMakeLists.txt": "cmake",
		"component.tsx":  "typescriptreact",
		"component.jsx":  "javascriptreact",
		"config.jsonc":   "jsonc",
		"types.pyi":      "python",
	}
	for path, want := range tests {
		if got := languageIDForPath(path); got != want {
			t.Errorf("languageIDForPath(%q) = %q, want %q", path, got, want)
		}
	}
}
