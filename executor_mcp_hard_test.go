package workflow

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPToolRunner_ConcurrentExecute hammers Execute from many goroutines
// to detect races on routes/sessions maps.
func TestMCPToolRunner_ConcurrentExecute(t *testing.T) {
	url := startTestServer(t, "concurrent", map[string]string{
		"ping": "pong",
	})

	runner := NewMCPToolRunner(map[string]string{"test": url})
	t.Cleanup(func() { _ = runner.Close() })

	if err := runner.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	var failures atomic.Int32

	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			result, err := runner.Execute(context.Background(), "ping", nil)
			if err != nil || result != "pong" {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()

	if f := failures.Load(); f > 0 {
		t.Errorf("%d/%d concurrent calls failed", f, goroutines)
	}
}

// TestMCPToolRunner_ToolNameCollision verifies last-server-wins when two
// servers expose the same tool name.
func TestMCPToolRunner_ToolNameCollision(t *testing.T) {
	urlA := startTestServer(t, "collision-a", map[string]string{"dup": "from A"})
	urlB := startTestServer(t, "collision-b", map[string]string{"dup": "from B"})

	runner := NewMCPToolRunner(map[string]string{
		"a": urlA,
		"b": urlB,
	})
	t.Cleanup(func() { _ = runner.Close() })

	if err := runner.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Should not panic or error — one of the servers wins.
	result, err := runner.Execute(context.Background(), "dup", nil)
	if err != nil {
		t.Fatalf("execute dup: %v", err)
	}
	if result != "from A" && result != "from B" {
		t.Errorf("unexpected result %q, want 'from A' or 'from B'", result)
	}
}

// TestMCPToolRunner_CloseAndExecute verifies that Execute after Close
// reconnects (lazy) or returns a clear error.
func TestMCPToolRunner_CloseAndExecute(t *testing.T) {
	url := startTestServer(t, "close-reuse", map[string]string{
		"ping": "pong",
	})

	runner := NewMCPToolRunner(map[string]string{"test": url})
	t.Cleanup(func() { _ = runner.Close() })

	if err := runner.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Verify works before close.
	if _, err := runner.Execute(context.Background(), "ping", nil); err != nil {
		t.Fatalf("pre-close execute: %v", err)
	}

	// Close all sessions.
	if err := runner.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Execute after close — routes are stale, session gone.
	// Should reconnect lazily via resolveServer → getSession.
	result, err := runner.Execute(context.Background(), "ping", nil)
	if err != nil {
		t.Fatalf("post-close execute: %v", err)
	}
	if result != "pong" {
		t.Errorf("got %q, want %q", result, "pong")
	}
}

// TestMCPToolRunner_CancelledContext verifies clean error on cancelled context.
func TestMCPToolRunner_CancelledContext(t *testing.T) {
	url := startTestServer(t, "cancel", map[string]string{"ping": "pong"})

	runner := NewMCPToolRunner(map[string]string{"test": url})
	t.Cleanup(func() { _ = runner.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := runner.Execute(ctx, "ping", nil)
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

// TestMCPToolRunner_InvalidURL verifies error on bad server URL.
func TestMCPToolRunner_InvalidURL(t *testing.T) {
	runner := NewMCPToolRunner(map[string]string{
		"bad": "http://127.0.0.1:1/mcp", // Nothing listening.
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := runner.Connect(ctx)
	if err == nil {
		t.Fatal("expected error connecting to invalid URL")
	}
}

// TestMCPToolRunner_MultipleTextParts verifies newline joining of multi-part content.
func TestMCPToolRunner_MultipleTextParts(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "multi-part", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "multi"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "line1"},
					&mcp.TextContent{Text: "line2"},
					&mcp.TextContent{Text: "line3"},
				},
			}, nil, nil
		})

	ts := mcpserver.NewTestServer(t, server, mcpserver.Config{
		Name: "multi-part", Version: "0.0.1", DisableRequestLog: true,
	})

	runner := NewMCPToolRunner(map[string]string{"test": ts.URL + "/mcp"})
	t.Cleanup(func() { _ = runner.Close() })

	result, err := runner.Execute(context.Background(), "multi", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result != "line1\nline2\nline3" {
		t.Errorf("got %q, want %q", result, "line1\nline2\nline3")
	}
}

// TestMCPToolRunner_NilArgs verifies tool call with nil arguments works.
func TestMCPToolRunner_NilArgs(t *testing.T) {
	url := startTestServer(t, "nil-args", map[string]string{"noargs": "ok"})

	runner := NewMCPToolRunner(map[string]string{"test": url})
	t.Cleanup(func() { _ = runner.Close() })

	result, err := runner.Execute(context.Background(), "noargs", nil)
	if err != nil {
		t.Fatalf("execute with nil args: %v", err)
	}
	if result != "ok" {
		t.Errorf("got %q, want %q", result, "ok")
	}
}

// TestMCPToolRunner_IsErrorContent verifies that IsError=true returns
// error with the tool's error message text.
func TestMCPToolRunner_IsErrorContent(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "err-content", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "fail_with_msg"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "custom error msg"}},
			}, nil, nil
		})

	ts := mcpserver.NewTestServer(t, server, mcpserver.Config{
		Name: "err-content", Version: "0.0.1", DisableRequestLog: true,
	})

	runner := NewMCPToolRunner(map[string]string{"test": ts.URL + "/mcp"})
	t.Cleanup(func() { _ = runner.Close() })

	_, err := runner.Execute(context.Background(), "fail_with_msg", nil)
	if err == nil {
		t.Fatal("expected error for IsError=true tool")
	}
	if !strings.Contains(err.Error(), "custom error msg") {
		t.Errorf("error %q should contain 'custom error msg'", err.Error())
	}
}
