package tray

import (
	"strings"
	"testing"
)

func TestStatusOf(t *testing.T) {
	st := statusOf([]byte(`{"sessions":[
		{"id":"a","model":"m1","busy":true,"flows":[
			{"sink":"LLM: Opus","model":"Opus","tokens":1500},
			{"sink":"LLM: Opus","model":"Opus","agentId":"s1","tokens":500},
			{"sink":"Shell"}]},
		{"id":"b","model":"gpt-5","busy":true,"waiting":true,"flows":[]},
		{"id":"c","model":"idle-model","busy":false,"flows":[{"sink":"LLM: Old","model":"Old","tokens":9}]},
		{"id":"d","waiting":true}]}`))
	if st.working != 1 || st.waiting != 2 {
		t.Fatalf("got %d working, %d waiting", st.working, st.waiting)
	}
	if len(st.models) != 2 || st.models[0].String() != "Opus — 2 agents · 2.0k tokens / 2 min" || st.models[1].String() != "gpt-5 — 1 agent" {
		t.Fatalf("models: %v", st.models)
	}
	if st := statusOf([]byte("x")); st.working != 0 || st.waiting != 0 || st.models != nil {
		t.Fatal("bad JSON must give an empty status")
	}
}

func TestJumpTargets(t *testing.T) {
	st := statusOf([]byte(`{"sessions":[
		{"kind":"copilot","id":"abc","name":"Fix\tthe build","surface":"Copilot app","cwd":"/r","pid":42,"waiting":true},
		{"kind":"copilot","id":"def","name":"","surface":"Copilot CLI","cwd":"/cli","pid":7,"waiting":true},
		{"kind":"vscode-chat","id":"x","name":"Chat","surface":"Code - Insiders chat","cwd":"/a","folders":["/w"],"waiting":true},
		{"kind":"copilot","id":"agent","name":"Agent","surface":"VS Code agent","cwd":"/repo/sub","pid":9,"waiting":true},
		{"kind":"copilot","id":"lost","name":"Lost","surface":"VS Code agent","cwd":"/elsewhere","pid":9,"waiting":true},
		{"kind":"copilot","id":"busy","surface":"Copilot CLI","busy":true}],
		"ides":[{"ide":"Some IDE","folders":["/repo"]},{"ide":"Visual Studio Code","folders":["/repo"]},{"ide":"Visual Studio Code - Insiders","folders":["/other","/repo/sub"]}]}`))
	if len(st.waits) != 5 {
		t.Fatalf("waits: %v", st.waits)
	}
	if got := st.waits[0].record(); got != "Fix the build — Copilot app\tghapp://sessions/abc\t42\t/r\t" {
		t.Fatalf("app: %q", got)
	}
	if got := st.waits[1].record(); got != "Session def — Copilot CLI\t\t7\t/cli\t" {
		t.Fatalf("cli: %q", got)
	}
	if got := st.waits[2].record(); got != "Chat — Code - Insiders chat\t\t0\t/w\tcom.microsoft.VSCodeInsiders" {
		t.Fatalf("vscode: %q", got)
	}
	if got := st.waits[3].record(); got != "Agent — VS Code agent\t\t9\t/repo/sub\tcom.microsoft.VSCodeInsiders" {
		t.Fatalf("vscode agent: %q", got)
	}
	if got := st.waits[4].record(); got != "Lost — VS Code agent\t\t9\t/elsewhere\t" {
		t.Fatalf("vscode agent without a window: %q", got)
	}
}

func TestModelIDAndDisplayNameMerge(t *testing.T) {
	state := `{"sessions":[
		{"id":"a","busy":true,"model":"claude-opus-5.5","flows":[{"sink":"LLM: Claude Opus 5.5","model":"Claude Opus 5.5","tokens":100}]},
		{"id":"b","busy":true,"model":"claude-opus-5.5"},
		{"id":"c","busy":true,"model":"00000000-0000-0000-0000-000000000000/example-model"},
		{"id":"d","busy":true,"flows":[{"sink":"LLM: example-model · BYOK","model":"example-model · BYOK","tokens":50}]}
	]}`
	st := statusOf([]byte(state))
	got := []string{}
	for _, m := range st.models {
		got = append(got, m.String())
	}
	want := []string{"Claude Opus 5.5 — 2 agents · 100 tokens / 2 min", "example-model · BYOK — 2 agents · 50 tokens / 2 min"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("models = %q, want %q", got, want)
	}
}
