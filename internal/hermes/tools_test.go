package hermes

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPToolNames(t *testing.T) {
	for _, tt := range []struct{ name, server, tool string }{
		{"mcp__demo__browser_navigate", "demo", "browser_navigate"},
		{"mcp__demo_server__browser_evaluate", "demo_server", "browser_evaluate"},
		{"mcp_demo_server_browser_navigate", "", ""},
		{"mcp__demo", "", ""},
		{"mcp____read", "", ""},
		{"mcp__demo__", "", ""},
		{"browser_navigate", "", ""},
	} {
		server, tool := mcpTool(tt.name)
		if server != tt.server || tool != tt.tool {
			t.Errorf("%q: got %q/%q, want %q/%q", tt.name, server, tool, tt.server, tt.tool)
		}
	}
}

func TestToolDiscoveryActivity(t *testing.T) {
	cfg := &Config{URL: "https://agent.example.com"}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}
	s := &Source{cfg: cfg}
	const now = int64(1_700_000_000_000)
	for _, arguments := range []string{
		`{"names":["mcp__demo__browser_navigate","mcp__demo__browser_evaluate"]}`,
		`"{\"names\":[\"mcp__demo__browser_navigate\",\"mcp__demo__browser_evaluate\"]}"`,
	} {
		var msgs []message
		input := `[{"role":"assistant","timestamp":1699999999,"tool_calls":[{"id":"describe-1","function":{"name":"tool_describe","arguments":` + arguments + `}}]}]`
		if err := json.Unmarshal([]byte(input), &msgs); err != nil {
			t.Fatal(err)
		}
		a := s.activity(msgs, "", false, now, "")
		if len(a.open) != 1 || len(a.flows) != 0 {
			t.Fatalf("discovery manufactured executions: %+v", a)
		}
		tool := a.open[0]
		want := "Describe tools: demo: browser navigate; demo: browser evaluate"
		if tool.Name != "tool_describe" || tool.Desc != want || tool.Sink != "Tool discovery: MCP: demo" {
			t.Fatalf("unexpected discovery: %+v", tool)
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(tool.RawDesc), &raw); err != nil || len(raw["names"].([]any)) != 2 {
			t.Fatalf("raw names not retained: %q", tool.RawDesc)
		}
		msgs = append(msgs, message{Role: "tool", ToolCallID: "describe-1", Timestamp: flexTime(now)})
		a = s.activity(msgs, "", false, now, "")
		if len(a.open) != 0 || len(a.flows) != 1 || a.flows[0].Tool != "tool_describe" ||
			a.flows[0].Sink != tool.Sink || a.flows[0].Desc != want || a.flows[0].RawDesc != tool.RawDesc {
			t.Fatalf("completed discovery lost identity or created target calls: %+v", a)
		}
	}
}

func TestToolBridgeFormatting(t *testing.T) {
	cfg := &Config{URL: "https://agent.example.com"}
	_ = cfg.normalize()
	s := &Source{cfg: cfg}
	for _, tt := range []struct {
		name, args, desc, sink string
	}{
		{"tool_describe", `{"names":"mcp__demo_server__read_file"}`, "Describe tools: demo_server: read file", "Tool discovery: MCP: demo_server"},
		{"tool_describe", `{"names":["mcp__alpha__read","mcp__beta__write","read_file"]}`, "Describe tools: alpha: read; beta: write; read_file", "Tool discovery: MCP: alpha; MCP: beta; other tools"},
		{"tool_describe", `{"names":["mcp_ambiguous_server_read"]}`, "Describe tools: mcp_ambiguous_server_read", "Tool discovery: MCP: unknown server"},
		{"tool_describe", `{"names":[42,null]}`, `{"names":[42,null]}`, "Tool discovery"},
		{"tool_describe", `{"names":[]}`, `{"names":[]}`, "Tool discovery"},
		{"tool_call", `{"calls":[{"name":"mcp__demo__browser_navigate","arguments":{"url":"https://example.com"}},{"name":"mcp__demo__browser_evaluate","arguments":{}}]}`, "Invoke tools: demo: browser navigate; demo: browser evaluate", "MCP: demo"},
		{"tool_call", `{"calls":[{"name":"mcp__alpha__read","arguments":{}},{"name":"mcp__beta__read","arguments":{}}]}`, "Invoke tools: alpha: read; beta: read", "MCP: alpha; MCP: beta"},
		{"tool_call", `{"calls":[{"name":"mcp_ambiguous_read","arguments":{}}]}`, "Invoke tools: mcp_ambiguous_read", "MCP: unknown server"},
		{"tool_call", `{"calls":[{"name":"web_extract","arguments":{"url":"https://example.com"}},{"name":"read_file","arguments":{"path":"demo.txt"}}]}`, "Invoke tools: web_extract; read_file", "Web; agent: Filesystem"},
		{"tool_call", `{"calls":[{"name":"tool_call","arguments":{}}]}`, "Invoke tools: tool_call", "agent: Tools"},
	} {
		var args map[string]any
		if err := json.Unmarshal([]byte(tt.args), &args); err != nil {
			t.Fatal(err)
		}

		desc, raw := describeTool(tt.name, args)
		if desc != tt.desc || s.sink(tt.name, args) != tt.sink {
			t.Errorf("%s %s: got %q / %q", tt.name, tt.args, desc, s.sink(tt.name, args))
		}
		if strings.HasPrefix(desc, "Describe tools:") || strings.HasPrefix(desc, "Invoke tools:") {
			if raw == "" {
				t.Error("formatted arguments lost raw inspection data")
			}
		}

	}
}

func TestMCPExecutionActivity(t *testing.T) {
	cfg := &Config{URL: "https://agent.example.com"}
	_ = cfg.normalize()
	s := &Source{cfg: cfg}
	const now = int64(1_700_000_000_000)
	var msgs []message
	if err := json.Unmarshal([]byte(`[{"role":"assistant","timestamp":1699999999,"tool_calls":[
				{"id":"call-1","function":{"name":"mcp__demo_server__browser_navigate","arguments":{"url":"https://example.com"}}},
				{"id":"batch-1","function":{"name":"tool_call","arguments":{"calls":[{"name":"mcp__demo_server__browser_evaluate","arguments":{}}]}}}
			]}]`), &msgs); err != nil {
		t.Fatal(err)
	}
	a := s.activity(msgs, "", false, now, "")
	if len(a.open) != 2 {
		t.Fatalf("missing recorded calls: %+v", a)
	}
	for _, call := range a.open {
		if call.Sink != "MCP: demo_server" {
			t.Fatalf("missing MCP attribution: %+v", call)
		}
	}
	if a.open[0].Name != "mcp__demo_server__browser_navigate" || a.open[1].Name != "tool_call" {
		t.Fatal("exact recorded call identifiers changed")
	}
	msgs = append(msgs, message{Role: "tool", ToolCallID: "batch-1", Timestamp: flexTime(now)})
	a = s.activity(msgs, "", false, now, "")
	if len(a.flows) != 1 || a.flows[0].Tool != "tool_call" || a.flows[0].Sink != "MCP: demo_server" {
		t.Fatalf("batch must stay one recorded call: %+v", a)
	}
}
