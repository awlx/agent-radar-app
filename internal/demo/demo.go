// Package demo makes mock agent data for screenshots and for a test of the UI.
// The data does not come from the computer. All names, paths, and prompts are examples.
package demo

import (
	"time"

	"github.com/awlx/copilot-radar/internal/collector"
)

// Source gives a new mock snapshot for each call. The flows move with the time, so that the UI shows activity.
type Source struct{}

func New() *Source { return &Source{} }

type flowSpec struct {
	sink, tool, desc, agent, model string
	every, offset                  int64 // milliseconds
	tokens                         int64
}

// recent makes the flows of the last 2 minutes. Each spec repeats every spec.every milliseconds.
func recent(now int64, specs []flowSpec) []collector.Flow {
	var out []collector.Flow
	for _, f := range specs {
		for at := now - (now-f.offset)%f.every; at > now-120_000; at -= f.every {
			fl := collector.Flow{Sink: f.sink, Tool: f.tool, Desc: f.desc, At: at, EndAt: at + 900, OK: true, AgentID: f.agent, Model: f.model}
			if f.tokens > 0 {
				fl.Tin, fl.Tout, fl.Tokens = f.tokens*9/10, f.tokens/10, f.tokens
			}
			out = append(out, fl)
		}
	}
	return out
}

func usage(model string, in, out, calls int64) map[string]*collector.ModelUsage {
	return map[string]*collector.ModelUsage{model: {In: in, Out: out, Calls: calls}}
}

func (*Source) Snapshot() *collector.Snapshot {
	now := time.Now().UnixMilli()
	ago := func(s int64) int64 { return now - s*1000 }
	const (
		sonnet = "Claude Sonnet 4.5"
		gpt    = "GPT-5"
		gemini = "Gemini 2.5 Pro"
		haiku  = "Claude Haiku 4.5"
	)

	app := &collector.Session{
		Kind: "copilot", ID: "7f3c1e52-demo-4a1b-9c0d-app000000001", Live: true, Surface: "Copilot app",
		Name: "Add rate limiting to the public API", Cwd: "/Users/demo/src/acme-api", Repo: "acme-api", Model: sonnet,
		Busy: true, LastEventAt: ago(2),
		LastUser:      "Add a token bucket rate limiter to the public API and cover it with tests.",
		LastAssistant: "I added the limiter middleware. Now I run the tests and ask a sub-agent to review the change.",
		Intent:        "Running the test suite",
		RunningTools:  []collector.Tool{{ID: "t1", Name: "bash", Desc: "$ go test ./...", StartedAt: ago(6), Sink: "Shell"}},
		Subagents: []collector.Subagent{
			{ID: "a1", Name: "explore", Type: "explore", Model: haiku, StartedAt: ago(40), LastAt: ago(3), Doing: "Finding HTTP handlers", Said: "Find all HTTP handlers that need the limiter.", Running: true},
			{ID: "a2", Name: "code-review", Type: "code-review", Model: gpt, StartedAt: ago(15), LastAt: ago(1), Doing: "Reviewing the rate limiter", Said: "Review the rate limiter change.", Running: true},
		},
		Flows: recent(now, []flowSpec{
			{sink: "LLM: " + sonnet, tool: "model", desc: sonnet, model: sonnet, every: 7000, offset: 0, tokens: 48_000},
			{sink: "Shell", tool: "bash", desc: "$ go test ./...", every: 11000, offset: 2000},
			{sink: "Filesystem", tool: "edit", desc: "internal/middleware/ratelimit.go", every: 9000, offset: 4000},
			{sink: "GitHub", tool: "bash", desc: "$ gh pr view 128", every: 23000, offset: 6000},
			{sink: "LLM: " + haiku, tool: "model", desc: haiku, agent: "a1", model: haiku, every: 5000, offset: 1000, tokens: 12_000},
			{sink: "Filesystem", tool: "grep", desc: "func .*Handler", agent: "a1", every: 6000, offset: 3000},
			{sink: "LLM: " + gpt, tool: "model", desc: gpt, agent: "a2", model: gpt, every: 8000, offset: 2500, tokens: 30_000},
		}),
		Peers:       []collector.Peer{{To: "a1", At: ago(40), Kind: "spawn", Text: "Find all HTTP handlers"}, {To: "a2", At: ago(15), Kind: "spawn", Text: "Review the change"}},
		Tokens:      collector.TokenCount{In: 1_840_000, Out: 61_000},
		ModelTokens: usage(sonnet, 1_840_000, 61_000, 74),
	}

	cli := &collector.Session{
		Kind: "copilot", ID: "c2a9d4f0-demo-4e7b-8a21-cli000000002", Live: true, Surface: "Copilot CLI",
		Name: "Fix the flaky checkout test", Cwd: "/Users/demo/src/storefront", Repo: "storefront", Model: gpt,
		Waiting: true, LastEventAt: ago(35),
		LastUser:      "The checkout test fails one time in ten. Find the cause.",
		LastAssistant: "The test uses the real clock. Do you want me to inject a fake clock, or to add a retry?",
		Intent:        "Waiting for your answer",
		RunningTools:  []collector.Tool{{ID: "t2", Name: "ask_user", Desc: "Inject a fake clock or add a retry?", StartedAt: ago(35), Sink: "You"}},
		Subagents:     []collector.Subagent{},
		Flows: recent(now, []flowSpec{
			{sink: "LLM: " + gpt, tool: "model", desc: gpt, model: gpt, every: 40000, offset: 0, tokens: 22_000},
			{sink: "Shell", tool: "bash", desc: "$ npm test -- checkout", every: 45000, offset: 9000},
		}),
		Peers:       []collector.Peer{},
		Tokens:      collector.TokenCount{In: 310_000, Out: 9_000},
		ModelTokens: usage(gpt, 310_000, 9_000, 18),
	}

	chat := &collector.Session{
		Kind: "vscode-chat", ID: "vscode-demo-chat-0003", Live: true, Surface: "Code chat",
		Name: "Explain the cache layer", Cwd: "/Users/demo/src/acme-web", Folders: []string{"/Users/demo/src/acme-web"}, Repo: "acme-web", Model: gemini,
		Busy: true, LastEventAt: ago(4),
		LastUser:      "How does the cache layer invalidate entries?",
		LastAssistant: "The cache keys contain the version of the record. A write increments the version.",
		Intent:        "Reading the cache code",
		RunningTools:  []collector.Tool{{ID: "t3", Name: "read_file", Desc: "src/cache/store.ts", StartedAt: ago(1), Sink: "Filesystem"}},
		Subagents:     []collector.Subagent{},
		Flows: recent(now, []flowSpec{
			{sink: "LLM: " + gemini, tool: "model", desc: gemini, model: gemini, every: 9000, offset: 500, tokens: 26_000},
			{sink: "Filesystem", tool: "read_file", desc: "src/cache/store.ts", every: 4000, offset: 1500},
		}),
		Peers:       []collector.Peer{},
		Tokens:      collector.TokenCount{In: 420_000, Out: 14_000},
		ModelTokens: usage(gemini, 420_000, 14_000, 0),
	}

	agent := &collector.Session{
		Kind: "copilot", ID: "a81be6c3-demo-4f02-b7d9-agent0000004", Live: true, Client: "vscode-agent-host", Surface: "VS Code agent",
		Name: "Update the docs for the new API version", Cwd: "/Users/demo/src/acme-docs", Repo: "acme-docs", Model: sonnet,
		Busy: true, LastEventAt: ago(3),
		LastUser:      "Update the API reference for version 2.",
		LastAssistant: "I read the change log. Now I get the current examples from the docs server.",
		Intent:        "Fetching the API examples",
		RunningTools:  []collector.Tool{{ID: "t4", Name: "docs-search", Desc: "rate limit headers", StartedAt: ago(2), Sink: "MCP: docs"}},
		Subagents:     []collector.Subagent{},
		Flows: recent(now, []flowSpec{
			{sink: "LLM: " + sonnet, tool: "model", desc: sonnet, model: sonnet, every: 10000, offset: 3000, tokens: 35_000},
			{sink: "MCP: docs", tool: "docs-search", desc: "rate limit headers", every: 8000, offset: 5000},
			{sink: "Web", tool: "web_fetch", desc: "https://example.com/changelog", every: 19000, offset: 7000},
		}),
		Peers:       []collector.Peer{},
		Tokens:      collector.TokenCount{In: 760_000, Out: 22_000},
		ModelTokens: usage(sonnet, 760_000, 22_000, 31),
	}

	idle := &collector.Session{
		Kind: "copilot", ID: "e4d07a19-demo-4c55-9e3e-idle00000005", Live: true, Surface: "Copilot CLI",
		Name: "Write the release notes", Cwd: "/Users/demo/src/acme-api", Repo: "acme-api", Model: sonnet,
		LastEventAt: ago(1800), LastUser: "Write the release notes for 2.4.", LastAssistant: "The release notes are in CHANGELOG.md.",
		RunningTools: []collector.Tool{}, Subagents: []collector.Subagent{}, Flows: []collector.Flow{}, Peers: []collector.Peer{},
		Tokens: collector.TokenCount{In: 90_000, Out: 4_000}, ModelTokens: usage(sonnet, 90_000, 4_000, 6),
	}

	return &collector.Snapshot{
		Now:      now,
		Sessions: []*collector.Session{app, cli, chat, agent, idle},
		IDEs: []collector.IDE{
			{ID: "demo-ide-1", IDE: "Code", Folders: []string{"/Users/demo/src/acme-web"}},
			{ID: "demo-ide-2", IDE: "Code", Folders: []string{"/Users/demo/src/acme-docs"}},
		},
	}
}
