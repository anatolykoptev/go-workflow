package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPToolRunner implements ToolRunner for remote MCP servers.
// Supports multiple servers with lazy connect and auto-discovery of tool names.
type MCPToolRunner struct {
	servers  map[string]string             // serverID → URL
	headers  map[string]map[string]string  // serverID → HTTP headers
	clients  map[string]*mcp.Client        // lazy-initialized
	sessions map[string]*mcp.ClientSession // lazy-initialized
	routes   map[string]string             // toolName → serverID
	mu       sync.RWMutex
}

// NewMCPToolRunner creates a runner for the given MCP servers.
// Connections are established lazily on first use or via Connect.
func NewMCPToolRunner(servers map[string]string) *MCPToolRunner {
	return &MCPToolRunner{
		servers:  servers,
		headers:  make(map[string]map[string]string),
		clients:  make(map[string]*mcp.Client),
		sessions: make(map[string]*mcp.ClientSession),
		routes:   make(map[string]string),
	}
}

// SetHeaders sets HTTP headers for a specific server (e.g. Authorization).
func (r *MCPToolRunner) SetHeaders(serverID string, headers map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers[serverID] = headers
}

// Execute calls a tool on the appropriate MCP server.
func (r *MCPToolRunner) Execute(ctx context.Context, name string, args map[string]any) (string, error) {
	serverID, err := r.resolveServer(ctx, name)
	if err != nil {
		return "", err
	}

	session, err := r.getSession(ctx, serverID)
	if err != nil {
		return "", fmt.Errorf("mcp connect %s: %w", serverID, err)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("mcp call %s.%s: %w", serverID, name, err)
	}

	text := extractText(result)

	if result.IsError {
		return "", fmt.Errorf("mcp tool %s error: %s", name, text)
	}

	return text, nil
}

// Connect explicitly connects to all servers and discovers their tools.
func (r *MCPToolRunner) Connect(ctx context.Context) error {
	for id := range r.servers {
		if _, err := r.getSession(ctx, id); err != nil {
			return fmt.Errorf("connect %s: %w", id, err)
		}
	}
	return r.discover(ctx)
}

// Tools returns discovered tool names grouped by server ID.
func (r *MCPToolRunner) Tools() map[string][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string][]string)
	for tool, server := range r.routes {
		result[server] = append(result[server], tool)
	}
	return result
}

// Close closes all MCP sessions.
func (r *MCPToolRunner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []string
	for id, session := range r.sessions {
		if err := session.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", id, err))
		}
	}

	r.sessions = make(map[string]*mcp.ClientSession)
	r.clients = make(map[string]*mcp.Client)

	if len(errs) > 0 {
		return fmt.Errorf("close sessions: %s", strings.Join(errs, "; "))
	}
	return nil
}

// resolveServer finds which server handles the given tool name.
func (r *MCPToolRunner) resolveServer(ctx context.Context, name string) (string, error) {
	r.mu.RLock()
	serverID, ok := r.routes[name]
	r.mu.RUnlock()
	if ok {
		return serverID, nil
	}

	// Tool not found — connect all servers and rediscover.
	for id := range r.servers {
		if _, err := r.getSession(ctx, id); err != nil {
			slog.Warn("mcp lazy connect failed", slog.String("server", id), slog.Any("error", err))
		}
	}
	if err := r.discover(ctx); err != nil {
		return "", err
	}

	r.mu.RLock()
	serverID, ok = r.routes[name]
	r.mu.RUnlock()
	if ok {
		return serverID, nil
	}

	return "", fmt.Errorf("mcp tool %q: no server registered", name)
}

// discover fetches tool lists from all connected servers.
func (r *MCPToolRunner) discover(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id := range r.servers {
		session, ok := r.sessions[id]
		if !ok {
			continue
		}

		result, err := session.ListTools(ctx, nil)
		if err != nil {
			slog.Warn("mcp discover failed", slog.String("server", id), slog.Any("error", err))
			continue
		}

		for _, t := range result.Tools {
			r.routes[t.Name] = id
		}
	}
	return nil
}

// headerTransport injects static headers into every request.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// getSession returns an existing session or creates a new one (double-check locking).
func (r *MCPToolRunner) getSession(ctx context.Context, serverID string) (*mcp.ClientSession, error) {
	r.mu.RLock()
	if session, ok := r.sessions[serverID]; ok {
		r.mu.RUnlock()
		return session, nil
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Double check after acquiring write lock.
	if session, ok := r.sessions[serverID]; ok {
		return session, nil
	}

	url, ok := r.servers[serverID]
	if !ok {
		return nil, fmt.Errorf("unknown mcp server: %s", serverID)
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "go-workflow",
		Version: "0.7.0",
	}, nil)

	httpClient := &http.Client{Timeout: 300 * time.Second}
	if hdrs, ok := r.headers[serverID]; ok && len(hdrs) > 0 {
		httpClient.Transport = &headerTransport{headers: hdrs}
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:             url,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s (%s): %w", serverID, url, err)
	}

	r.clients[serverID] = client
	r.sessions[serverID] = session

	slog.Info("mcp client connected", slog.String("server", serverID), slog.String("url", url))

	return session, nil
}

// extractText collects text content from an MCP call result.
func extractText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	if len(parts) == 0 {
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}
	return strings.Join(parts, "\n")
}
