package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// --- n8n JSON types ---

// N8nWorkflow represents a complete n8n workflow JSON export.
type N8nWorkflow struct {
	Name        string                       `json:"name"`
	Nodes       []N8nNode                    `json:"nodes"`
	Connections map[string]N8nNodeConnection `json:"connections"`
	Settings    map[string]any               `json:"settings,omitempty"`
	Tags        []N8nTag                     `json:"tags,omitempty"`
}

// N8nNode represents a single node in an n8n workflow.
type N8nNode struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Type             string         `json:"type"`
	TypeVersion      int            `json:"typeVersion,omitempty"`
	Parameters       map[string]any `json:"parameters"`
	Position         [2]int         `json:"position,omitempty"`
	RetryOnFail      bool           `json:"retryOnFail,omitempty"`
	MaxTries         int            `json:"maxTries,omitempty"`
	WaitBetweenTries int            `json:"waitBetweenTries,omitempty"`
	ContinueOnFail   bool           `json:"continueOnFail,omitempty"`
}

// N8nNodeConnection holds the output connections for a node.
// Format: {"main": [[{"node": "Name", "type": "main", "index": 0}]]}
type N8nNodeConnection struct {
	Main [][]N8nConnectionTarget `json:"main"`
}

// N8nConnectionTarget identifies a downstream node.
type N8nConnectionTarget struct {
	Node  string `json:"node"`
	Type  string `json:"type"`
	Index int    `json:"index"`
}

// N8nTag is a workflow tag.
type N8nTag struct {
	Name string `json:"name"`
}

// --- Conversion ---

// ConvertN8nToTemplate parses an n8n workflow JSON and converts it to a Vaelor Template.
func ConvertN8nToTemplate(data []byte) (*Template, error) {
	var n8n N8nWorkflow
	if err := json.Unmarshal(data, &n8n); err != nil {
		return nil, fmt.Errorf("parse n8n json: %w", err)
	}

	if len(n8n.Nodes) == 0 {
		return nil, errors.New("n8n workflow has no nodes")
	}

	// Build name→node and name→id maps
	nodeByName := make(map[string]*N8nNode, len(n8n.Nodes))
	nameToID := make(map[string]string, len(n8n.Nodes))
	for i := range n8n.Nodes {
		node := &n8n.Nodes[i]
		nodeByName[node.Name] = node
		nodeID := stableID(node)
		nameToID[node.Name] = nodeID
	}

	// Build depends_on from connections (reverse: connections map source→targets)
	// We need target→sources
	dependsOn := make(map[string][]string) // nodeID → []upstream nodeIDs
	for sourceName, conn := range n8n.Connections {
		sourceID := nameToID[sourceName]
		for _, outputs := range conn.Main {
			for _, target := range outputs {
				targetID := nameToID[target.Node]
				if targetID != "" && sourceID != "" {
					dependsOn[targetID] = appendUnique(dependsOn[targetID], sourceID)
				}
			}
		}
	}

	// Convert nodes to steps (skip triggers, they become metadata)
	var steps []TemplateStep
	triggerParams := map[string]string{} // extracted from trigger nodes

	for i := range n8n.Nodes {
		node := &n8n.Nodes[i]
		nodeID := nameToID[node.Name]

		// Skip trigger nodes — extract metadata
		if isTriggerNode(node.Type) {
			extractTriggerParams(node, triggerParams)
			// Remove this node from depends_on of downstream nodes
			// (their deps should be empty = root steps)
			for tid, deps := range dependsOn {
				dependsOn[tid] = removeFromSlice(deps, nodeID)
			}
			continue
		}

		step := convertNode(node, nodeID, dependsOn[nodeID], nameToID)
		steps = append(steps, step)
	}

	if len(steps) == 0 {
		return nil, errors.New("n8n workflow has no actionable nodes (only triggers)")
	}

	// Build template
	tmpl := &Template{
		Name:        n8n.Name,
		Description: buildDescription(n8n),
		Steps:       steps,
		Params:      triggerParams,
		Defaults:    make(map[string]any),
	}

	return tmpl, nil
}

// ConvertN8nFile reads an n8n JSON file and converts it to a Vaelor Template.
func ConvertN8nFile(path string) (*Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read n8n file: %w", err)
	}
	return ConvertN8nToTemplate(data)
}

// --- Node type classification ---

var triggerTypes = map[string]bool{
	"n8n-nodes-base.webhook":         true,
	"n8n-nodes-base.scheduleTrigger": true,
	"n8n-nodes-base.manualTrigger":   true,
	"n8n-nodes-base.cronTrigger":     true,
	"n8n-nodes-base.emailTrigger":    true,
	"n8n-nodes-base.start":           true,
}

func isTriggerNode(nodeType string) bool {
	if triggerTypes[nodeType] {
		return true
	}
	return strings.HasSuffix(nodeType, "Trigger")
}

// --- Node → Step conversion ---

func convertNode(node *N8nNode, nodeID string, deps []string, nameToID map[string]string) TemplateStep {
	kind, config := mapNodeToStep(node, nameToID)

	// Convert retry settings
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

// mapNodeToStep maps an n8n node type to a Vaelor step kind and config.
func mapNodeToStep(node *N8nNode, nameToID map[string]string) (StepKind, map[string]any) {
	nodeType := node.Type

	switch nodeType {
	// HTTP Request → check if it's a Vaelor API call, otherwise use web_fetch
	case "n8n-nodes-base.httpRequest":
		return convertHTTPRequest(node, nameToID)

	// Code node → agent task
	case "n8n-nodes-base.code":
		return convertCodeNode(node)

	// Condition → condition step
	case "n8n-nodes-base.if", "n8n-nodes-base.switch":
		return convertConditionNode(node, nameToID)

	// Telegram → message step
	case "n8n-nodes-base.telegram":
		return convertTelegramNode(node, nameToID)

	// Set/Merge → agent task (data transformation)
	case "n8n-nodes-base.set", "n8n-nodes-base.merge":
		return convertDataNode(node, nameToID)

	// Function (legacy) → agent task
	case "n8n-nodes-base.function", "n8n-nodes-base.functionItem":
		return convertCodeNode(node)

	// Wait → agent task with sleep instruction
	case "n8n-nodes-base.wait":
		return StepAgent, map[string]any{
			"task":            fmt.Sprintf("Wait/delay step from n8n node '%s'. Parameters: %s", node.Name, mustJSON(node.Parameters)),
			"timeout_seconds": 30,
			"max_iterations":  1,
		}

	// Default: unknown node → agent fallback
	default:
		return convertUnknownNode(node)
	}
}

// convertHTTPRequest converts an HTTP Request node.
// If it's calling the Vaelor API, extract the tool call directly.
func convertHTTPRequest(node *N8nNode, nameToID map[string]string) (StepKind, map[string]any) {
	urlStr, _ := node.Parameters["url"].(string)

	// Detect Vaelor API tool call pattern
	if strings.Contains(urlStr, "/tools/execute") {
		toolName, toolArgs := extractVaelorToolCall(node)
		if toolName != "" {
			config := map[string]any{
				"tool": toolName,
			}
			if toolArgs != nil {
				config["args"] = toolArgs
			}
			return StepTool, config
		}
	}

	// Detect Vaelor agent API call
	if strings.Contains(urlStr, "/agent/run") {
		task := extractAgentTask(node)
		if task != "" {
			config := map[string]any{
				"task": convertN8nExpressions(task, nameToID),
			}
			if model := extractBodyParam(node, "model"); model != "" {
				config["model"] = model
			}
			if timeout := extractBodyParam(node, "timeout_seconds"); timeout != "" {
				config["timeout_seconds"] = 300
			}
			config["max_iterations"] = 15
			return StepAgent, config
		}
	}

	// Generic HTTP request → http_request tool (supports all methods, headers, body)
	method, _ := node.Parameters["method"].(string)
	if method == "" {
		method = "GET"
	}

	httpArgs := map[string]any{
		"url":    convertN8nExpressions(urlStr, nameToID),
		"method": method,
	}

	// Extract headers
	if headers, ok := node.Parameters["headerParameters"].(map[string]any); ok {
		if params, ok := headers["parameters"].([]any); ok && len(params) > 0 {
			h := map[string]any{}
			for _, p := range params {
				if pm, ok := p.(map[string]any); ok {
					name, _ := pm["name"].(string)
					value, _ := pm["value"].(string)
					if name != "" {
						h[name] = convertN8nExpressions(value, nameToID)
					}
				}
			}
			if len(h) > 0 {
				httpArgs["headers"] = h
			}
		}
	}

	// Extract query parameters
	if queryParams, ok := node.Parameters["queryParameters"].(map[string]any); ok {
		if params, ok := queryParams["parameters"].([]any); ok && len(params) > 0 {
			qp := map[string]any{}
			for _, p := range params {
				if pm, ok := p.(map[string]any); ok {
					name, _ := pm["name"].(string)
					value, _ := pm["value"].(string)
					if name != "" {
						qp[name] = convertN8nExpressions(value, nameToID)
					}
				}
			}
			if len(qp) > 0 {
				httpArgs["query_params"] = qp
			}
		}
	}

	// Extract authentication
	if authType, ok := node.Parameters["authentication"].(string); ok && authType != "none" {
		httpArgs["auth_type"] = authType
	}

	// Extract timeout
	if opts, ok := node.Parameters["options"].(map[string]any); ok {
		if timeout, ok := opts["timeout"].(float64); ok {
			httpArgs["timeout_seconds"] = int(timeout / 1000)
		}
	}

	config := map[string]any{
		"tool": ToolHTTPRequest,
		"args": httpArgs,
	}
	return StepTool, config
}

// convertCodeNode converts a Code/Function node to an agent task.
func convertCodeNode(node *N8nNode) (StepKind, map[string]any) {
	code := ""
	if js, ok := node.Parameters["jsCode"].(string); ok {
		code = js
	} else if js, ok := node.Parameters["functionCode"].(string); ok {
		code = js
	}

	task := fmt.Sprintf("Execute the following data transformation logic (from n8n node '%s'):\n\n```javascript\n%s\n```\n\nProcess the input data according to this logic and return the result as JSON.", node.Name, code)

	return StepAgent, map[string]any{
		"task":            task,
		"timeout_seconds": 60,
		"max_iterations":  5,
	}
}

// convertConditionNode converts an If/Switch node to a condition step.
func convertConditionNode(node *N8nNode, nameToID map[string]string) (StepKind, map[string]any) {
	// Try to extract a simple condition from n8n parameters
	config := map[string]any{
		"not_empty": true,
	}

	// n8n conditions structure: {"conditions": {"string": [{"value1": "...", "operation": "isNotEmpty"}]}}
	if conditions, ok := node.Parameters["conditions"].(map[string]any); ok {
		for _, checks := range conditions {
			if arr, ok := checks.([]any); ok && len(arr) > 0 {
				if check, ok := arr[0].(map[string]any); ok {
					val1, _ := check["value1"].(string)
					op, _ := check["operation"].(string)

					checkRef := convertN8nExpressions(val1, nameToID)
					config["check"] = checkRef

					switch op {
					case "isNotEmpty", "exists":
						config["not_empty"] = true
					case "contains":
						if val2, ok := check["value2"].(string); ok {
							config["contains"] = val2
							delete(config, "not_empty")
						}
					case "equals", "equal":
						if val2, ok := check["value2"].(string); ok {
							config["equals"] = val2
							delete(config, "not_empty")
						}
					default:
						config["not_empty"] = true
					}
				}
			}
			break // take first condition group only
		}
	}

	return StepCondition, config
}

// convertTelegramNode converts a Telegram node to a message step.
func convertTelegramNode(node *N8nNode, nameToID map[string]string) (StepKind, map[string]any) {
	text, _ := node.Parameters["text"].(string)
	text = convertN8nExpressions(text, nameToID)

	return StepMessage, map[string]any{
		"content": text,
	}
}

// convertDataNode converts Set/Merge nodes to a transform step.
func convertDataNode(node *N8nNode, nameToID map[string]string) (StepKind, map[string]any) {
	config := map[string]any{}

	switch node.Type {
	case "n8n-nodes-base.set":
		// Extract values to set from n8n Set node parameters
		setValues := map[string]any{}
		if vals, ok := node.Parameters["values"].(map[string]any); ok {
			for dataType, items := range vals {
				if arr, ok := items.([]any); ok {
					for _, item := range arr {
						if m, ok := item.(map[string]any); ok {
							name, _ := m["name"].(string)
							value := m["value"]
							if name != "" {
								_ = dataType
								if s, ok := value.(string); ok {
									setValues[name] = convertN8nExpressions(s, nameToID)
								} else {
									setValues[name] = value
								}
							}
						}
					}
				}
			}
		}
		if len(setValues) > 0 {
			config["set"] = setValues
		}

	case "n8n-nodes-base.merge":
		// Merge node combines results from multiple inputs
		config["merge"] = []string{} // will be populated from depends_on at runtime
	}

	return StepTransform, config
}

// convertUnknownNode converts any unrecognized n8n node type to an agent fallback.
func convertUnknownNode(node *N8nNode) (StepKind, map[string]any) {
	paramsJSON := mustJSON(node.Parameters)
	task := fmt.Sprintf(
		"Execute the functionality of n8n node type '%s' (name: '%s'). "+
			"Node parameters: %s\n\n"+
			"Determine what this node does based on its type and parameters, "+
			"use available tools to accomplish the same result, and return the output.",
		node.Type, node.Name, paramsJSON,
	)

	return StepAgent, map[string]any{
		"task":            task,
		"timeout_seconds": 120,
		"max_iterations":  10,
	}
}

// --- Expression conversion ---

// n8nExprRegex matches n8n expressions like ={{ ... }}
var n8nExprRegex = regexp.MustCompile(`=\{\{([^}]+)\}\}`)

// n8nNodeRefRegex matches $node["Name"].json.field or $node['Name'].json.field
var n8nNodeRefRegex = regexp.MustCompile(`\$node\[["']([^"']+)["']\]\.json(?:\.(\w+))?`)

// n8nJsonFieldRegex matches $json.field
var n8nJsonFieldRegex = regexp.MustCompile(`\$json\.(\w+)`)

// n8nEnvRegex matches $env.VAR_NAME
var n8nEnvRegex = regexp.MustCompile(`\$env\.(\w+)`)

// convertN8nExpressions replaces n8n expression patterns with Vaelor context refs.
func convertN8nExpressions(s string, nameToID map[string]string) string {
	if s == "" {
		return s
	}

	// Replace $node["Name"] references → {{node_id}}
	s = n8nNodeRefRegex.ReplaceAllStringFunc(s, func(match string) string {
		groups := n8nNodeRefRegex.FindStringSubmatch(match)
		if len(groups) >= 2 {
			nodeName := groups[1]
			if id, ok := nameToID[nodeName]; ok {
				return "{{" + id + "}}"
			}
		}
		return match
	})

	// Replace ={{ expr }} → attempt to simplify
	s = n8nExprRegex.ReplaceAllStringFunc(s, func(match string) string {
		inner := n8nExprRegex.FindStringSubmatch(match)
		if len(inner) < 2 {
			return match
		}
		expr := strings.TrimSpace(inner[1])

		// $env.VAR → ${VAR} (will be resolved at runtime)
		if n8nEnvRegex.MatchString(expr) {
			return n8nEnvRegex.ReplaceAllString(expr, "${$1}")
		}

		// Simple $json.field → keep as template expression (agent will interpret)
		if n8nJsonFieldRegex.MatchString(expr) {
			return expr
		}

		// Complex expression → keep as-is for agent interpretation
		return expr
	})

	return s
}

// --- Helper: extract Vaelor tool call from HTTP Request node ---

func extractVaelorToolCall(node *N8nNode) (string, map[string]any) {
	// Look in bodyParameters for tool name and args
	bodyParams, ok := node.Parameters["bodyParameters"].(map[string]any)
	if !ok {
		return "", nil
	}

	params, ok := bodyParams["parameters"].([]any)
	if !ok {
		return "", nil
	}

	var toolName string
	var argsStr string

	for _, p := range params {
		param, ok := p.(map[string]any)
		if !ok {
			continue
		}
		name, _ := param["name"].(string)
		value, _ := param["value"].(string)

		switch name {
		case "tool":
			toolName = value
		case "args":
			argsStr = value
		}
	}

	if toolName == "" {
		return "", nil
	}

	// Try to parse args JSON
	var args map[string]any
	if argsStr != "" {
		// Strip n8n expression wrapper if present
		clean := strings.TrimPrefix(argsStr, "=")
		clean = strings.TrimSpace(clean)
		if err := json.Unmarshal([]byte(clean), &args); err != nil {
			// Keep as string arg for agent to interpret
			args = map[string]any{"_raw": argsStr}
		}
	}

	return toolName, args
}

func extractAgentTask(node *N8nNode) string {
	bodyParams, ok := node.Parameters["bodyParameters"].(map[string]any)
	if !ok {
		return ""
	}
	params, ok := bodyParams["parameters"].([]any)
	if !ok {
		return ""
	}
	for _, p := range params {
		param, ok := p.(map[string]any)
		if !ok {
			continue
		}
		name, _ := param["name"].(string)
		value, _ := param["value"].(string)
		if name == "task" {
			return value
		}
	}
	return ""
}

func extractBodyParam(node *N8nNode, key string) string {
	bodyParams, ok := node.Parameters["bodyParameters"].(map[string]any)
	if !ok {
		return ""
	}
	params, ok := bodyParams["parameters"].([]any)
	if !ok {
		return ""
	}
	for _, p := range params {
		param, ok := p.(map[string]any)
		if !ok {
			continue
		}
		name, _ := param["name"].(string)
		value, _ := param["value"].(string)
		if name == key {
			return value
		}
	}
	return ""
}

// --- Trigger parameter extraction ---

func extractTriggerParams(node *N8nNode, params map[string]string) {
	switch node.Type {
	case "n8n-nodes-base.webhook":
		if path, ok := node.Parameters["path"].(string); ok {
			params["webhook_path"] = path
		}
		if method, ok := node.Parameters["httpMethod"].(string); ok {
			params["webhook_method"] = method
		}
	case "n8n-nodes-base.scheduleTrigger", "n8n-nodes-base.cronTrigger":
		if rule, ok := node.Parameters["rule"].(map[string]any); ok {
			if intervals, ok := rule["interval"].([]any); ok && len(intervals) > 0 {
				if interval, ok := intervals[0].(map[string]any); ok {
					if expr, ok := interval["expression"].(string); ok {
						params["cron"] = expr
					}
				}
			}
		}
	}
}

// --- Helpers ---

func stableID(node *N8nNode) string {
	if node.ID != "" {
		return node.ID
	}
	// Fallback: slugify the name
	id := strings.ToLower(node.Name)
	id = strings.ReplaceAll(id, " ", "_")
	id = regexp.MustCompile(`[^a-z0-9_]`).ReplaceAllString(id, "")
	if id == "" {
		id = "node"
	}
	return id
}

func buildDescription(n8n N8nWorkflow) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Converted from n8n workflow '%s'", n8n.Name))

	if len(n8n.Tags) > 0 {
		var tagNames []string
		for _, t := range n8n.Tags {
			tagNames = append(tagNames, t.Name)
		}
		parts = append(parts, "Tags: "+strings.Join(tagNames, ", "))
	}

	parts = append(parts, fmt.Sprintf("Original nodes: %d", len(n8n.Nodes)))
	return strings.Join(parts, ". ")
}

func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

func removeFromSlice(slice []string, item string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}
