package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ConvertN8nToTemplate parses an n8n workflow JSON and converts it to a workflow Template.
func ConvertN8nToTemplate(data []byte) (*Template, error) {
	var n8n N8nWorkflow
	if err := json.Unmarshal(data, &n8n); err != nil {
		return nil, fmt.Errorf("parse n8n json: %w", err)
	}
	if len(n8n.Nodes) == 0 {
		return nil, errors.New("n8n workflow has no nodes")
	}

	nameToID := buildNameToID(n8n.Nodes)
	dependsOn := buildDependsOn(n8n.Connections, nameToID)

	steps, triggerParams := convertNodes(n8n.Nodes, nameToID, dependsOn)
	if len(steps) == 0 {
		return nil, errors.New("n8n workflow has no actionable nodes (only triggers)")
	}

	return &Template{
		Name:        n8n.Name,
		Description: buildDescription(n8n),
		Steps:       steps,
		Params:      triggerParams,
		Defaults:    make(map[string]any),
	}, nil
}

func buildNameToID(nodes []N8nNode) map[string]string {
	m := make(map[string]string, len(nodes))
	for i := range nodes {
		m[nodes[i].Name] = stableID(&nodes[i])
	}
	return m
}

func buildDependsOn(conns map[string]N8nNodeConnection, nameToID map[string]string) map[string][]string {
	deps := make(map[string][]string)
	for sourceName, conn := range conns {
		sourceID := nameToID[sourceName]
		for _, outputs := range conn.Main {
			for _, target := range outputs {
				targetID := nameToID[target.Node]
				if targetID != "" && sourceID != "" {
					deps[targetID] = appendUnique(deps[targetID], sourceID)
				}
			}
		}
	}
	return deps
}

func convertNodes(nodes []N8nNode, nameToID map[string]string, dependsOn map[string][]string) ([]TemplateStep, ParamsMap) {
	var steps []TemplateStep
	triggerParams := ParamsMap{}

	for i := range nodes {
		node := &nodes[i]
		nodeID := nameToID[node.Name]

		if isTriggerNode(node.Type) {
			extractTriggerParams(node, triggerParams)
			for tid, deps := range dependsOn {
				dependsOn[tid] = removeFromSlice(deps, nodeID)
			}
			continue
		}
		steps = append(steps, convertNode(node, nodeID, dependsOn[nodeID], nameToID))
	}
	return steps, triggerParams
}

// ConvertN8nFile reads an n8n JSON file and converts it to a workflow Template.
func ConvertN8nFile(path string) (*Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read n8n file: %w", err)
	}
	return ConvertN8nToTemplate(data)
}

// --- Node → Step conversion ---

func convertNode(node *N8nNode, nodeID string, deps []string, nameToID map[string]string) TemplateStep {
	kind, config := mapNodeToStep(node, nameToID)

	var retryJSON json.RawMessage
	if node.RetryOnFail && node.MaxTries > 0 {
		delayMs := node.WaitBetweenTries
		if delayMs <= 0 {
			delayMs = 1000
		}
		retryData, _ := json.Marshal(map[string]any{
			"max":      node.MaxTries,
			"delay_ms": delayMs,
		})
		retryJSON = retryData
	}

	onError := ""
	if node.ContinueOnFail {
		onError = OnErrorSkip
	}

	configJSON, _ := json.Marshal(config)

	return TemplateStep{
		ID:        nodeID,
		Kind:      kind,
		Config:    configJSON,
		DependsOn: deps,
		Retry:     retryJSON,
		OnError:   onError,
	}
}

// mapNodeToStep maps an n8n node type to a step kind and config.
func mapNodeToStep(node *N8nNode, nameToID map[string]string) (StepKind, map[string]any) {
	switch node.Type {
	case "n8n-nodes-base.httpRequest":
		return convertHTTPRequest(node, nameToID)
	case "n8n-nodes-base.code":
		return convertCodeNode(node)
	case "n8n-nodes-base.if", "n8n-nodes-base.switch":
		return convertConditionNode(node, nameToID)
	case "n8n-nodes-base.telegram":
		return convertTelegramNode(node, nameToID)
	case "n8n-nodes-base.set", "n8n-nodes-base.merge":
		return convertDataNode(node, nameToID)
	case "n8n-nodes-base.function", "n8n-nodes-base.functionItem":
		return convertCodeNode(node)
	case "n8n-nodes-base.wait":
		return StepAgent, map[string]any{
			"task":            fmt.Sprintf("Wait/delay step from n8n node '%s'. Parameters: %s", node.Name, mustJSON(node.Parameters)),
			"timeout_seconds": 30,
			"max_iterations":  1,
		}
	default:
		return convertUnknownNode(node)
	}
}
