package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Regression: the self-call server (go-wp) has NO static headers — the
// identity transport must still be installed or X-MCP-User never reaches
// the authnz middleware and the call runs as the loopback operator.
func TestMCPSession_StampsIdentityWithoutStaticHeaders(t *testing.T) {
	var gotUser string

	srv := mcp.NewServer(&mcp.Implementation{Name: "stub", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
			if e := req.GetExtra(); e != nil {
				gotUser = e.Header.Get("X-MCP-User")
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, struct{}{}, nil
		})

	httpSrv := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv }, nil))
	defer httpSrv.Close()

	// No SetHeaders call — mirrors the go-wp self-server wiring.
	runner := NewMCPToolRunner(map[string]string{"self": httpSrv.URL + "/mcp"})
	defer runner.Close()

	ctx := withMCPIdentity(context.Background(), "fiesta")
	out, err := runner.Execute(ctx, "ping", map[string]any{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "pong" {
		t.Fatalf("out = %q, want pong", out)
	}
	if gotUser != "fiesta" {
		t.Errorf("server saw X-MCP-User=%q, want fiesta", gotUser)
	}
}

// Concurrent workflows sharing the cached session must not leak identity:
// each request stamps from its own ctx.
func TestMCPSession_NoIdentityLeakAcrossWorkflows(t *testing.T) {
	got := make(chan string, 4)

	srv := mcp.NewServer(&mcp.Implementation{Name: "stub", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
			u := ""
			if e := req.GetExtra(); e != nil {
				u = e.Header.Get("X-MCP-User")
			}
			got <- u
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, struct{}{}, nil
		})

	httpSrv := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv }, nil))
	defer httpSrv.Close()

	runner := NewMCPToolRunner(map[string]string{"self": httpSrv.URL + "/mcp"})
	defer runner.Close()

	owners := []string{"fiesta", "anatoly", "fiesta", "anatoly"}
	done := make(chan error, len(owners))
	for _, o := range owners {
		go func(owner string) {
			_, err := runner.Execute(withMCPIdentity(context.Background(), owner), "ping", map[string]any{})
			done <- err
		}(o)
	}
	for range owners {
		if err := <-done; err != nil {
			t.Fatalf("execute: %v", err)
		}
	}
	close(got)

	seen := map[string]int{}
	for u := range got {
		seen[u]++
	}
	if seen["fiesta"] != 2 || seen["anatoly"] != 2 {
		t.Errorf("identity mixup: %v", seen)
	}
}
