package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchReadsTextResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("documentation"))
	}))
	defer server.Close()
	tool := &webFetchTool{client: server.Client(), allowPrivate: true}
	result, err := tool.Execute(context.Background(), map[string]any{"url": server.URL}, Context{
		Interaction: &stubInteraction{decision: ApprovalAllowOnce},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content, "documentation") || !strings.Contains(result.Content, "200 OK") {
		t.Fatalf("unexpected fetch result: %#v", result)
	}
}

func TestWebFetchRejectsLocalAndNonHTTPURLs(t *testing.T) {
	tool := NewWebFetchTool()
	for _, target := range []string{"file:///etc/passwd", "http://127.0.0.1/private", "http://localhost/private"} {
		if _, err := tool.Execute(context.Background(), map[string]any{"url": target}, Context{}); err == nil {
			t.Fatalf("WebFetch accepted %s", target)
		}
	}
}
