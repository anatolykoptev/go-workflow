package workflow

import (
	"context"
	"errors"
	"testing"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startTestServer creates a test MCP server with the given tools.
func startTestServer(t *testing.T, name string, tools map[string]string) string {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: name, Version: "0.0.1"}, nil)

	for toolName, response := range tools {
		resp := response
		mcp.AddTool(server, &mcp.Tool{Name: toolName},
			func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: resp}},
				}, nil, nil
			})
	}

	ts := mcpserver.NewTestServer(t, server, mcpserver.Config{
		Name:              name,
		Version:           "0.0.1",
		DisableRequestLog: true,
	})

	return ts.URL + "/mcp"
}

func TestMCPToolRunner_SingleServer(t *testing.T) {
	url := startTestServer(t, "single", map[string]string{
		"greet": "hello world",
	})

	runner := NewMCPToolRunner(map[string]string{"test": url})
	t.Cleanup(func() { _ = runner.Close() })

	if err := runner.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	result, err := runner.Execute(context.Background(), "greet", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result != "hello world" {
		t.Errorf("got %q, want %q", result, "hello world")
	}
}

func TestMCPToolRunner_MultiServer(t *testing.T) {
	urlA := startTestServer(t, "server-a", map[string]string{
		"tool_a": "from A",
	})
	urlB := startTestServer(t, "server-b", map[string]string{
		"tool_b": "from B",
	})

	runner := NewMCPToolRunner(map[string]string{
		"a": urlA,
		"b": urlB,
	})
	t.Cleanup(func() { _ = runner.Close() })

	if err := runner.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Verify routing.
	tools := runner.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 servers, got %d: %v", len(tools), tools)
	}

	resultA, err := runner.Execute(context.Background(), "tool_a", nil)
	if err != nil {
		t.Fatalf("execute tool_a: %v", err)
	}
	if resultA != "from A" {
		t.Errorf("tool_a: got %q, want %q", resultA, "from A")
	}

	resultB, err := runner.Execute(context.Background(), "tool_b", nil)
	if err != nil {
		t.Fatalf("execute tool_b: %v", err)
	}
	if resultB != "from B" {
		t.Errorf("tool_b: got %q, want %q", resultB, "from B")
	}
}

func TestMCPToolRunner_ToolNotFound(t *testing.T) {
	url := startTestServer(t, "empty", map[string]string{
		"exists": "ok",
	})

	runner := NewMCPToolRunner(map[string]string{"test": url})
	t.Cleanup(func() { _ = runner.Close() })

	if err := runner.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	_, err := runner.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent tool")
	}
}

func TestMCPToolRunner_ServerError(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "err-server", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "failing"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return nil, nil, errors.New("something broke")
		})

	ts := mcpserver.NewTestServer(t, server, mcpserver.Config{
		Name:              "err-server",
		Version:           "0.0.1",
		DisableRequestLog: true,
	})

	runner := NewMCPToolRunner(map[string]string{"test": ts.URL + "/mcp"})
	t.Cleanup(func() { _ = runner.Close() })

	if err := runner.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}

	_, err := runner.Execute(context.Background(), "failing", nil)
	if err == nil {
		t.Fatal("expected error from failing tool")
	}
}

func TestMCPToolRunner_LazyConnect(t *testing.T) {
	url := startTestServer(t, "lazy", map[string]string{
		"ping": "pong",
	})

	// Do NOT call Connect — Execute should trigger lazy connect + discovery.
	runner := NewMCPToolRunner(map[string]string{"test": url})
	t.Cleanup(func() { _ = runner.Close() })

	result, err := runner.Execute(context.Background(), "ping", nil)
	if err != nil {
		t.Fatalf("lazy execute: %v", err)
	}
	if result != "pong" {
		t.Errorf("got %q, want %q", result, "pong")
	}
}
