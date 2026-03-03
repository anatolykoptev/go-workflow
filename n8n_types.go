package workflow

import (
	"encoding/json"
	"fmt"
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

// --- Regex patterns for n8n expression conversion ---

// n8nExprRegex matches n8n expressions like ={{ ... }}
var n8nExprRegex = regexp.MustCompile(`=\{\{([^}]+)\}\}`)

// n8nNodeRefRegex matches $node["Name"].json.field or $node['Name'].json.field
var n8nNodeRefRegex = regexp.MustCompile(`\$node\[["']([^"']+)["']\]\.json(?:\.(\w+))?`)

// n8nJsonFieldRegex matches $json.field
var n8nJsonFieldRegex = regexp.MustCompile(`\$json\.(\w+)`)

// n8nEnvRegex matches $env.VAR_NAME
var n8nEnvRegex = regexp.MustCompile(`\$env\.(\w+)`)

// --- Helpers ---

func stableID(node *N8nNode) string {
	if node.ID != "" {
		return node.ID
	}
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
