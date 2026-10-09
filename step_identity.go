package workflow

import "context"

// runAsOperator marks a tool step that must execute with the deployment's
// operator privileges rather than the workflow owner's identity. The value
// lives in step config ("run_as": "operator") and is template-author
// controlled -- every other tool step runs as the workflow owner so that
// downstream MCP authz sees the real caller (per-user creds, tool
// allowlists, per-user audit). Approval steps stay the gate for anything
// sensitive: a privileged tool step is only reachable after the approval
// step(s) the template placed before it.
const runAsOperator = "operator"

// mcpIdentityHeader is the HTTP header the MCP transport stamps with the
// step identity. MCP servers behind an identity-aware edge (e.g. go-wp's
// authnz middleware) resolve it to a caller.
const mcpIdentityHeader = "X-MCP-User"

type ctxMCPIdentity struct{}

// withMCPIdentity carries an identity into MCP tool calls. Empty identity
// is a no-op so operator-owned workflows keep the historical behavior.
func withMCPIdentity(ctx context.Context, identity string) context.Context {
	if identity == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxMCPIdentity{}, identity)
}

// mcpIdentityFrom extracts the identity stamped by withMCPIdentity.
func mcpIdentityFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxMCPIdentity{}).(string)
	return id
}
