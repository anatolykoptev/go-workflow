package workflow

import "fmt"

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
	config := map[string]any{"not_empty": true}

	check := extractFirstCondition(node)
	if check != nil {
		applyConditionOp(config, check, nameToID)
	}

	return StepCondition, config
}

// extractFirstCondition gets the first condition check from an n8n If/Switch node.
func extractFirstCondition(node *N8nNode) map[string]any {
	conditions, ok := node.Parameters["conditions"].(map[string]any)
	if !ok {
		return nil
	}
	for _, checks := range conditions {
		arr, ok := checks.([]any)
		if !ok || len(arr) == 0 {
			continue
		}
		check, ok := arr[0].(map[string]any)
		if ok {
			return check
		}
		break
	}
	return nil
}

// applyConditionOp applies the condition operation to the step config.
func applyConditionOp(config, check map[string]any, nameToID map[string]string) {
	val1, _ := check["value1"].(string)
	op, _ := check["operation"].(string)
	config["check"] = convertN8nExpressions(val1, nameToID)

	switch op {
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
		if setValues := extractSetValues(node, nameToID); len(setValues) > 0 {
			config["set"] = setValues
		}
	case "n8n-nodes-base.merge":
		config["merge"] = []string{}
	}

	return StepTransform, config
}

// extractSetValues parses name/value pairs from an n8n Set node.
func extractSetValues(node *N8nNode, nameToID map[string]string) map[string]any {
	vals, ok := node.Parameters["values"].(map[string]any)
	if !ok {
		return nil
	}

	result := map[string]any{}
	for _, items := range vals {
		arr, ok := items.([]any)
		if !ok {
			continue
		}
		extractSetItems(arr, nameToID, result)
	}
	return result
}

func extractSetItems(arr []any, nameToID map[string]string, result map[string]any) {
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		if s, ok := m["value"].(string); ok {
			result[name] = convertN8nExpressions(s, nameToID)
		} else {
			result[name] = m["value"]
		}
	}
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
