package tray

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestLinuxWindowFailureCleansScript(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("PATH", tmp)
	if err := runLinuxWindow(context.Background(), Options{URL: "http://127.0.0.1:47900/"}); err == nil {
		t.Fatal("missing GJS must be reported")
	}
	files, err := os.ReadDir(tmp)
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary native script was not removed: %v, %v", files, err)
	}
}

func TestLinuxTargetCommand(t *testing.T) {
	for _, tt := range []struct {
		target jumpTarget
		name   string
		args   []string
	}{
		{jumpTarget{url: "ghapp://sessions/demo"}, "xdg-open", []string{"ghapp://sessions/demo"}},
		{jumpTarget{editor: "Code", folder: "/demo/folder with spaces"}, "code", []string{"--reuse-window", "--", "/demo/folder with spaces"}},
		{jumpTarget{editor: "Code - Insiders", folder: "/demo/a.code-workspace"}, "code-insiders", []string{"--reuse-window", "--", "/demo/a.code-workspace"}},
		{jumpTarget{pid: 123}, "", nil},
		{jumpTarget{editor: "Code", pid: 123}, "", nil},
		{jumpTarget{editor: "Unknown", folder: "/demo"}, "", nil},
	} {
		name, args := linuxTargetCommand(tt.target)
		if name != tt.name || !reflect.DeepEqual(args, tt.args) {
			t.Errorf("command = %q %q, want %q %q", name, args, tt.name, tt.args)
		}
	}
}

func TestLinuxMenuJumpCapabilities(t *testing.T) {
	state := []byte(`{"now":123,"sessions":[
		{"id":"app","surface":"Copilot app","waiting":true},
		{"id":"cli","surface":"Copilot CLI","pid":123,"waiting":true},
		{"id":"saved","kind":"vscode-chat","surface":"Code chat","folders":["/demo"],"waiting":true},
		{"id":"unsaved","kind":"vscode-chat","surface":"Code chat","folders":["/demo/a","/demo/b"],"waiting":true}]}`)
	st, err := MenuState(state, true)
	if err != nil || st.Waiting != 4 || st.Working != 0 {
		t.Fatalf("status: %+v, %v", st, err)
	}
	for i, want := range []bool{true, false, true, false} {
		if st.Waits[i].Jumpable != want {
			t.Errorf("target %s jumpable = %v", st.Waits[i].ID, st.Waits[i].Jumpable)
		}
	}
	st, err = MenuState(state, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, wait := range st.Waits {
		if wait.Jumpable {
			t.Fatal("demo/disabled navigation must remain disabled")
		}
	}
	if _, err := MenuState([]byte("invalid"), true); err == nil {
		t.Fatal("invalid status must have an error")
	}
}
