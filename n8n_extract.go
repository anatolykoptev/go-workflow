package workflow

import (
	"encoding/json"
	"strings"
)

// --- Expression conversion ---

// convertN8nExpressions replaces n8n expression patterns with tool context refs.
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

// --- HTTP request parameter extraction ---

// convertHTTPRequest converts an HTTP Request node.
// If it is calling the tool API (WORKFLOW_TOOL_API_URL), extract the tool/agent call directly.
func convertHTTPRequest(node *N8nNode, nameToID map[string]string) (StepKind, map[string]any) {
	urlStr, _ := node.Parameters["url"].(string)

	if kind, config, ok := tryConvertToolAPI(node, urlStr, nameToID); ok {
		return kind, config
	}

	return convertGenericHTTP(node, urlStr, nameToID)
}

func tryConvertToolAPI(node *N8nNode, urlStr string, nameToID map[string]string) (StepKind, map[string]any, bool) {
	if strings.Contains(urlStr, "/tools/execute") {
		toolName, toolArgs := extractToolCall(node)
		if toolName != "" {
			config := map[string]any{"tool": toolName}
			if toolArgs != nil {
				config["args"] = toolArgs
			}
			return StepTool, config, true
		}
	}

	if strings.Contains(urlStr, "/agent/run") {
		if kind, config, ok := tryConvertAgentCall(node, nameToID); ok {
			return kind, config, true
		}
	}
	return "", nil, false
}

func tryConvertAgentCall(node *N8nNode, nameToID map[string]string) (StepKind, map[string]any, bool) {
	task := extractAgentTask(node)
	if task == "" {
		return "", nil, false
	}
	config := map[string]any{
		"task":           convertN8nExpressions(task, nameToID),
		"max_iterations": 15,
	}
	if model := extractBodyParam(node, "model"); model != "" {
		config["model"] = model
	}
	if timeout := extractBodyParam(node, "timeout_seconds"); timeout != "" {
		config["timeout_seconds"] = 300
	}
	return StepAgent, config, true
}

func convertGenericHTTP(node *N8nNode, urlStr string, nameToID map[string]string) (StepKind, map[string]any) {
	method, _ := node.Parameters["method"].(string)
	if method == "" {
		method = "GET"
	}

	httpArgs := map[string]any{
		"url":    convertN8nExpressions(urlStr, nameToID),
		"method": method,
	}

	if h := extractN8nNameValueParams(node, "headerParameters", nameToID); len(h) > 0 {
		httpArgs["headers"] = h
	}
	if qp := extractN8nNameValueParams(node, "queryParameters", nameToID); len(qp) > 0 {
		httpArgs["query_params"] = qp
	}
	if authType, ok := node.Parameters["authentication"].(string); ok && authType != "none" {
		httpArgs["auth_type"] = authType
	}
	if opts, ok := node.Parameters["options"].(map[string]any); ok {
		if timeout, ok := opts["timeout"].(float64); ok {
			httpArgs["timeout_seconds"] = int(timeout / 1000)
		}
	}

	return StepTool, map[string]any{"tool": ToolHTTPRequest, "args": httpArgs}
}

// extractN8nNameValueParams extracts name/value pairs from n8n node parameters
// (used for headerParameters, queryParameters). Deduplicates the iteration logic.
func extractN8nNameValueParams(node *N8nNode, key string, nameToID map[string]string) map[string]any {
	container, ok := node.Parameters[key].(map[string]any)
	if !ok {
		return nil
	}
	params, ok := container["parameters"].([]any)
	if !ok || len(params) == 0 {
		return nil
	}

	result := map[string]any{}
	for _, p := range params {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		name, _ := pm["name"].(string)
		value, _ := pm["value"].(string)
		if name != "" {
			result[name] = convertN8nExpressions(value, nameToID)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// --- tool API extraction helpers (WORKFLOW_TOOL_API_URL) ---

func extractToolCall(node *N8nNode) (string, map[string]any) {
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

	var args map[string]any
	if argsStr != "" {
		clean := strings.TrimPrefix(argsStr, "=")
		clean = strings.TrimSpace(clean)
		if err := json.Unmarshal([]byte(clean), &args); err != nil {
			args = map[string]any{"_raw": argsStr}
		}
	}

	return toolName, args
}

func extractAgentTask(node *N8nNode) string {
	return extractBodyParam(node, "task")
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

func extractTriggerParams(node *N8nNode, params ParamsMap) {
	switch node.Type {
	case "n8n-nodes-base.webhook":
		if path, ok := node.Parameters["path"].(string); ok {
			params["webhook_path"] = ParamSpec{Type: "string", Description: path}
		}
		if method, ok := node.Parameters["httpMethod"].(string); ok {
			params["webhook_method"] = ParamSpec{Type: "string", Description: method}
		}
	case "n8n-nodes-base.scheduleTrigger", "n8n-nodes-base.cronTrigger":
		if expr := extractCronExpression(node); expr != "" {
			params["cron"] = ParamSpec{Type: "string", Description: expr}
		}
	}
}

func extractCronExpression(node *N8nNode) string {
	rule, ok := node.Parameters["rule"].(map[string]any)
	if !ok {
		return ""
	}
	intervals, ok := rule["interval"].([]any)
	if !ok || len(intervals) == 0 {
		return ""
	}
	interval, ok := intervals[0].(map[string]any)
	if !ok {
		return ""
	}
	expr, _ := interval["expression"].(string)
	return expr
}
