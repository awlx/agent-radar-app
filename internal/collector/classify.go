package collector

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Sink classification: where does a tool call send or fetch data?

var bashSinks = []struct {
	re   *regexp.Regexp
	sink string
}{
	{regexp.MustCompile(`^(gh)\b`), "GitHub"},
	{regexp.MustCompile(`^(git)\b`), "Git"},
	{regexp.MustCompile(`^(curl|wget|http)\b`), "Web"},
	{regexp.MustCompile(`^(ssh|scp|rsync|mosh)\b`), "SSH hosts"},
	{regexp.MustCompile(`^(docker|podman|kubectl|helm)\b`), "Containers"},
	{regexp.MustCompile(`^(go|cargo|npm|npx|yarn|pnpm|make|bazel|python3?|pip|node|ruby|bundle|rake)\b`), "Build / run"},
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var (
	fileTools    = set("view", "edit", "create", "apply_patch", "grep", "glob", "rg", "str_replace_editor", "read_file", "write_file")
	agentTools   = set("task", "read_agent", "write_agent", "list_agents")
	sessionTools = set("send_session_message", "create_session", "open_pr_session", "open_issue_session", "get_session",
		"get_sessions_status", "list_sessions_and_chats", "answer_session_input", "respond_to_session_plan_v2")
	reCdPrefix   = regexp.MustCompile(`^(cd\s+\S+\s*(&&|;)\s*)+`)
	reSudoPrefix = regexp.MustCompile(`^(sudo|env\s+\S+=\S+)\s+`)
)

func firstCmd(cmd string) string {
	c := strings.TrimSpace(cmd)
	c = reCdPrefix.ReplaceAllString(c, "")
	return reSudoPrefix.ReplaceAllString(c, "")
}

// classifyTool returns the data destination of a tool call, or "" when the
// call is not a data flow (sub-agent and session tools are drawn as edges).
// mcpServer is the MCP server name from the event (data.mcpServerName), if known. Explicit MCP
// metadata comes first, because an MCP tool can have the name of a built-in tool.
// known is the set of MCP server names that the session used before.
func classifyTool(name string, args map[string]any, mcpServer string, known map[string]bool) string {
	switch {
	case name == "":
		return ""
	case mcpServer != "":
		return "MCP: " + mcpServer
	case name == "bash" || name == "powershell" || name == "shell" || name == "run_in_terminal":
		c := firstCmd(str(args["command"]))
		for _, b := range bashSinks {
			if b.re.MatchString(c) {
				return b.sink
			}
		}
		return "Shell"
	case fileTools[name]:
		return "Filesystem"
	case name == "web_fetch" || name == "web_search" || name == "fetch_webpage":
		return "Web"
	case name == "lexical_code_search" || name == "semantic_code_search":
		return "GitHub"
	case agentTools[name], sessionTools[name]:
		return ""
	case name == "ask_user":
		return "You"
	case name == "sql" || name == "session_store_sql":
		return "Session DB"
	// Copilot Memory of the CLI and the app is hosted by GitHub.
	case name == "store_memory" || name == "vote_memory":
		return "Copilot Memory"
	}
	if strings.Index(name, "-") > 0 {
		if srv := mcpServerOf(name, known); srv != "" {
			return "MCP: " + srv
		}
		return "MCP: unknown server"
	}
	return ""
}

var reMCPMarker = regexp.MustCompile(`^(.+?-mcp(?:-server)?)-.`)

// mcpServerOf gives the MCP server of a tool name "<server>-<tool>" without data.mcpServerName: the
// longest server name that the session used before, else the name up to a "-mcp-server-" or "-mcp-" marker.
func mcpServerOf(name string, known map[string]bool) string {
	best := ""
	for k := range known {
		if len(k) > len(best) && strings.HasPrefix(name, k+"-") {
			best = k
		}
	}
	if best != "" {
		return best
	}
	if m := reMCPMarker.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

func describeArgs(a map[string]any) string {
	if a == nil {
		return ""
	}
	if c, ok := a["command"].(string); ok {
		return "$ " + c
	}
	if p, pa := str(a["pattern"]), str(a["path"]); p != "" && pa != "" {
		return trunc(p+"  in "+pa, 400)
	}
	for _, k := range []string{"path", "url", "query", "pattern", "description", "prompt", "message", "title"} {
		if v := str(a[k]); v != "" {
			return trunc(v, 400)
		}
	}
	b, _ := json.Marshal(a)
	return trunc(string(b), 400)
}

var reSpaces = regexp.MustCompile(`\s+`)

func trunc(s string, n int) string {
	s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

var reDrive = regexp.MustCompile(`^[A-Za-z]:(/|$)`)

// normPath gives a path with "/" separators. Windows paths do not use case.
func normPath(p string) string {
	n := strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
	if reDrive.MatchString(n) {
		n = strings.ToLower(n)
	}
	return n
}

// InFolder tells if cwd is folder or a path in folder.
func InFolder(cwd, folder string) bool {
	c, f := normPath(cwd), normPath(folder)
	return c != "" && f != "" && (c == f || strings.HasPrefix(c, f+"/"))
}

// sessionRoots gives the root folders of a session. A multi-root VS Code window has more than one.
func sessionRoots(s *Session) []string {
	src := s.Folders
	if len(src) == 0 {
		src = []string{s.Cwd}
	}
	out := []string{}
	for _, f := range src {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
