package hermes

import (
	"encoding/json"
	"strings"

	"github.com/awlx/agent-radar-app/internal/collector"
)

// Hermes uses mcp__<server>__<tool>. Its legacy single-underscore form has
// no unambiguous server boundary, so it must not supply guessed attribution.
func mcpTool(name string) (server, tool string) {
	if !strings.HasPrefix(name, "mcp__") {
		return "", ""
	}
	server, tool, ok := strings.Cut(strings.TrimPrefix(name, "mcp__"), "__")
	if !ok || server == "" || tool == "" {
		return "", ""
	}
	return server, tool
}

func toolNames(name string, args map[string]any) []string {
	var names []string
	switch name {
	case "tool_describe":
		switch value := args["names"].(type) {
		case string:
			names = append(names, value)
		case []any:
			for _, entry := range value {
				if text, ok := entry.(string); ok {
					names = append(names, text)
				}
			}
		}
	case "tool_call":
		if calls, ok := args["calls"].([]any); ok {
			for _, entry := range calls {
				if call, ok := entry.(map[string]any); ok {
					if text, ok := call["name"].(string); ok {
						names = append(names, text)
					}
				}
			}
		}
	}
	return names
}

func describeTool(name string, args map[string]any) (desc, raw string) {
	names := toolNames(name, args)
	if len(names) == 0 {
		return collector.DescribeArgs(args), ""
	}
	labels := make([]string, 0, len(names))
	for _, name := range names {
		server, tool := mcpTool(name)
		if server != "" {
			labels = append(labels, server+": "+strings.ReplaceAll(tool, "_", " "))
		} else {
			labels = append(labels, name)
		}
	}
	prefix := "Describe tools: "
	if name == "tool_call" {
		prefix = "Invoke tools: "
	}
	b, _ := json.Marshal(args)
	return collector.Trunc(prefix+strings.Join(labels, "; "), 400), string(b)
}

func discoverySink(names []string) string {
	var servers []string
	seen := map[string]bool{}
	for _, name := range names {
		server, _ := mcpTool(name)
		if server == "" {
			if strings.HasPrefix(name, "mcp_") {
				server = "MCP: unknown server"
			} else {
				server = "other tools"
			}
		} else {
			server = "MCP: " + server
		}
		if !seen[server] {
			seen[server] = true
			servers = append(servers, server)
		}
	}
	if len(servers) == 0 {
		return "Tool discovery"
	}
	return "Tool discovery: " + strings.Join(servers, "; ")
}
