package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPDeps holds the dependencies required by MCP tool handlers.
type MCPDeps struct {
	Engine    *Engine
	Templates *TemplateStore
}

// Input types.

type wfCreateInput struct {
	Template string         `json:"template" jsonschema:"Template name"`
	Owner    string         `json:"owner"    jsonschema:"Workflow owner"`
	Params   map[string]any `json:"params"   jsonschema:"Template parameters"`
}

type wfStatusInput struct {
	WorkflowID string `json:"workflow_id" jsonschema:"Workflow ID"`
}

type wfApproveInput struct {
	WorkflowID string         `json:"workflow_id" jsonschema:"Workflow ID"`
	Approved   bool           `json:"approved"    jsonschema:"Approve or reject"`
	Data       map[string]any `json:"data"        jsonschema:"Optional approval data"`
	StepID     string         `json:"step_id"     jsonschema:"Optional: target a specific approval step by id. Omit to resolve the single blocking gate automatically (BlockingStep)."`
}

type wfListInput struct {
	State string `json:"state" jsonschema:"Filter by state (empty = active)"`
}

type wfCancelInput struct {
	WorkflowID string `json:"workflow_id" jsonschema:"Workflow ID"`
}

type wfReopenInput struct {
	WorkflowID string `json:"workflow_id" jsonschema:"Workflow ID"`
}

type wfTemplatesInput struct{}

// Output types.

type wfCreateOutput struct {
	ID       string   `json:"id"`
	Template string   `json:"template"`
	Steps    int      `json:"steps"`
	StepIDs  []string `json:"step_ids"`
}

type wfStepSummary struct {
	ID    string    `json:"id"`
	Kind  StepKind  `json:"kind"`
	State StepState `json:"state"`
	Error string    `json:"error,omitempty"`
}

type wfStatusOutput struct {
	ID              string          `json:"id"`
	State           WorkflowState   `json:"state"`
	Steps           []wfStepSummary `json:"steps"`
	PendingApproval string          `json:"pending_approval,omitempty"`
	Error           string          `json:"error,omitempty"`
}

type wfTemplateInfo struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Params      ParamsMap `json:"params,omitempty"`
}

// Helpers.

func textResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

func errResult(msg string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}, nil, nil
}

// RegisterMCPTools registers all workflow MCP tools on the given server
// and returns the number of tools registered.
func RegisterMCPTools(server *mcp.Server, deps MCPDeps) int {
	tools := [...]func(*mcp.Server, MCPDeps){
		registerWFCreate,
		registerWFStatus,
		registerWFApprove,
		registerWFList,
		registerWFCancel,
		registerWFReopen,
		registerWFTemplates,
	}
	for _, fn := range tools {
		fn(server, deps)
	}
	return len(tools)
}

func registerWFCreate(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{Name: "wf_create", Description: "Create and start a workflow from a template"},
		func(ctx context.Context, _ *mcp.CallToolRequest, input wfCreateInput) (*mcp.CallToolResult, any, error) {
			if input.Template == "" {
				return errResult("template is required")
			}
			// Validate engine has executors for every kind this template needs
			// before instantiating — fails fast with a wiring hint instead of
			// the cryptic "no executor for step kind" at first run.
			if tmpl, ok := deps.Templates.Get(input.Template); ok {
				if err := deps.Engine.ValidateTemplate(tmpl); err != nil {
					return errResult(err.Error())
				}
			}
			wfID := fmt.Sprintf("wf-%s-%d", input.Template, time.Now().UnixMilli())
			wf, err := deps.Templates.Instantiate(input.Template, wfID, input.Owner, input.Params)
			if err != nil {
				return errResult(fmt.Sprintf("instantiate: %v", err))
			}
			if err := deps.Engine.Store().Save(wf); err != nil {
				return errResult(fmt.Sprintf("save: %v", err))
			}
			if err := deps.Engine.StartAsync(ctx, wfID); err != nil {
				return errResult(fmt.Sprintf("start: %v", err))
			}
			ids := make([]string, len(wf.Steps))
			for i, s := range wf.Steps {
				ids[i] = s.ID
			}
			return textResult(wfCreateOutput{ID: wfID, Template: input.Template, Steps: len(wf.Steps), StepIDs: ids})
		})
}

func registerWFStatus(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{Name: "wf_status", Description: "Get workflow status and step summary"},
		func(_ context.Context, _ *mcp.CallToolRequest, input wfStatusInput) (*mcp.CallToolResult, any, error) {
			wf, ok := deps.Engine.Store().Load(input.WorkflowID)
			if !ok {
				return errResult(fmt.Sprintf("workflow %q not found", input.WorkflowID))
			}
			summaries := make([]wfStepSummary, len(wf.Steps))
			for i, s := range wf.Steps {
				summaries[i] = wfStepSummary{ID: s.ID, Kind: s.Kind, State: s.State, Error: s.Error}
			}
			// pending_approval is derived from the authoritative CurrentStep via
			// BlockingStep, not an independent scan — see issue #23. Keeps this
			// field in sync with HandleApproval/HandleApprovalWithData's target.
			var pending string
			if gate := wf.BlockingStep(); gate != nil && gate.Kind == StepApproval && gate.State == StepPending {
				pending = gate.ID
			}
			return textResult(wfStatusOutput{ID: wf.ID, State: wf.State, Steps: summaries, PendingApproval: pending, Error: wf.Error})
		})
}

func registerWFApprove(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{Name: "wf_approve", Description: "Approve or reject a waiting workflow"},
		func(ctx context.Context, _ *mcp.CallToolRequest, input wfApproveInput) (*mcp.CallToolResult, any, error) {
			if input.Data != nil {
				if err := deps.Engine.HandleApprovalWithData(input.WorkflowID, input.Approved, input.Data, input.StepID); err != nil {
					return errResult(fmt.Sprintf("approval: %v", err))
				}
			} else {
				if err := deps.Engine.HandleApproval(input.WorkflowID, input.Approved, input.StepID); err != nil {
					return errResult(fmt.Sprintf("approval: %v", err))
				}
			}
			if input.Approved {
				deps.Engine.ResumeAsync(ctx, input.WorkflowID)
			}
			wf, ok := deps.Engine.Store().Load(input.WorkflowID)
			if !ok {
				return errResult(fmt.Sprintf("workflow %q not found after approval", input.WorkflowID))
			}
			return textResult(map[string]any{"workflow_id": input.WorkflowID, "state": wf.State})
		})
}

func registerWFList(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{Name: "wf_list", Description: "List workflows by state"},
		func(_ context.Context, _ *mcp.CallToolRequest, input wfListInput) (*mcp.CallToolResult, any, error) {
			var workflows []*Workflow
			if input.State != "" {
				workflows = deps.Engine.Store().List(WorkflowState(input.State))
			} else {
				for _, st := range []WorkflowState{StateRunning, StateWaitingApproval, StatePending, StatePaused} {
					workflows = append(workflows, deps.Engine.Store().List(st)...)
				}
			}
			items := make([]map[string]any, len(workflows))
			for i, wf := range workflows {
				items[i] = map[string]any{"id": wf.ID, "template": wf.TemplateName, "state": wf.State, "owner": wf.Owner}
			}
			return textResult(items)
		})
}

func registerWFCancel(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{Name: "wf_cancel", Description: "Cancel a running workflow"},
		func(_ context.Context, _ *mcp.CallToolRequest, input wfCancelInput) (*mcp.CallToolResult, any, error) {
			if err := deps.Engine.Cancel(input.WorkflowID); err != nil {
				return errResult(fmt.Sprintf("cancel: %v", err))
			}
			return textResult(map[string]any{"workflow_id": input.WorkflowID, "state": StateCancelled})
		})
}

func registerWFReopen(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{Name: "wf_reopen", Description: "Reopen a cancelled workflow back to waiting_approval (only if it has a pending approval step to resume)"},
		func(_ context.Context, _ *mcp.CallToolRequest, input wfReopenInput) (*mcp.CallToolResult, any, error) {
			if err := deps.Engine.Reopen(input.WorkflowID); err != nil {
				return errResult(fmt.Sprintf("reopen: %v", err))
			}
			return textResult(map[string]any{"workflow_id": input.WorkflowID, "state": StateWaitingApproval})
		})
}

func registerWFTemplates(server *mcp.Server, deps MCPDeps) {
	mcp.AddTool(server, &mcp.Tool{Name: "wf_templates", Description: "List available workflow templates"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ wfTemplatesInput) (*mcp.CallToolResult, any, error) {
			if deps.Templates == nil {
				return textResult([]wfTemplateInfo{})
			}
			names := deps.Templates.List()
			infos := make([]wfTemplateInfo, 0, len(names))
			for _, name := range names {
				tmpl, ok := deps.Templates.Get(name)
				if !ok {
					continue
				}
				infos = append(infos, wfTemplateInfo{Name: tmpl.Name, Description: tmpl.Description, Params: tmpl.Params})
			}
			return textResult(infos)
		})
}
