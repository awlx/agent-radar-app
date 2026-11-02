package tray

import (
	"encoding/json"
	"fmt"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/awlx/agent-radar-app/internal/collector"
)

// Source gives the JSON state of the radar and the updates of the state.
type Source interface {
	State() []byte
	Subscribe() chan []byte
	Unsubscribe(chan []byte)
}

// Options are the settings of the app.
type Options struct {
	URL     string // the URL of the radar UI
	Version string
	// ShowWindow shows the radar window at the start. Without it, the app starts with the tray icon only.
	ShowWindow bool
	// Linux's primary GTK instance starts or reuses the backend before showing the graph.
	Backend string
	Demo    bool
	Poll    time.Duration
}

// trayStatus is the data for the menu bar item.
type trayStatus struct {
	working, waiting int
	models           []trayModel
	waits            []jumpTarget
}

// jumpTarget tells the app how to show a session that waits for the user.
// The app opens url if it is set. Else it opens folder in the editor, or it finds the app of pid.
// editor is the name of the data folder of a VS Code editor, for example "Code - Insiders".
type jumpTarget struct {
	title, url, folder, editor string
	pid                        int
}

// editorBundles gives the macOS bundle IDs of the VS Code editors, by the name of their data folder.
var editorBundles = map[string]string{
	"Code":            "com.microsoft.VSCode",
	"Code - Insiders": "com.microsoft.VSCodeInsiders",
	"Cursor":          "com.todesktop.230313mzl4w4u92",
	"VSCodium":        "com.vscodium",
}

// ideEditors gives the name of the data folder of a VS Code editor, by the ideName in its ~/.copilot/ide lock file.
var ideEditors = map[string]string{
	"Visual Studio Code":            "Code",
	"Visual Studio Code - Insiders": "Code - Insiders",
	"Cursor":                        "Cursor",
	"VSCodium":                      "VSCodium",
}

func jumpTo(kind, id, surface, name, cwd, workspace string, folders []string, pid int) jumpTarget {
	title := strings.Join(strings.Fields(name), " ")
	if title == "" {
		title = "Session " + id[:min(8, len(id))]
	}
	t := jumpTarget{title: truncate(title, 60) + " — " + surface, folder: cwd, pid: pid}
	if len(folders) > 0 {
		t.folder = windowPath(folders, workspace)
	}
	switch {
	case surface == "Copilot app":
		t.url = "ghapp://sessions/" + url.PathEscape(id)
	case kind == "vscode-chat":
		t.editor = strings.TrimSuffix(surface, " chat")
	}
	return t
}

// windowPath gives the path that the editor opens to show a VS Code window: the saved workspace file,
// or the folder. An unsaved multi-root window has no such path, because the editor opens a folder of
// it in a new window. Then the path is "", and the editor only comes to the front.
func windowPath(folders []string, workspace string) string {
	if workspace != "" || len(folders) != 1 {
		return workspace
	}
	return folders[0]
}

// record encodes the target as one line with tab-separated fields for the Objective-C code.
func (t jumpTarget) record() string {
	clean := func(s string) string { return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s) }
	return strings.Join([]string{clean(t.title), clean(t.url), strconv.Itoa(t.pid), clean(t.folder), editorBundles[t.editor]}, "\t")
}

// trayModel is a model that active agents use now.
type trayModel struct {
	name   string
	agents int
	tokens int64 // tokens in the last 2 minutes
}

func (m trayModel) String() string {
	s := m.name + " — " + plural(m.agents, "agent")
	if m.tokens > 0 {
		s += " · " + shortCount(m.tokens) + " tokens / 2 min"
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func shortCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// radarState is the part of the JSON state of the radar that the app uses.
type radarState struct {
	Now      int64          `json:"now"`
	Sessions []radarSession `json:"sessions"`
	IDEs     []struct {
		IDE       string   `json:"ide"`
		Folders   []string `json:"folders"`
		Workspace string   `json:"workspace"`
	} `json:"ides"`
}

// MenuSnapshot contains only the data needed by an external status icon.
type MenuSnapshot struct {
	Schema  int           `json:"schema"`
	Now     int64         `json:"now"`
	Working int           `json:"working"`
	Waiting int           `json:"waiting"`
	Waits   []MenuSession `json:"waits"`
}

type MenuSession struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Jumpable bool   `json:"jumpable"`
}

func MenuState(state []byte, jumpEnabled bool) (MenuSnapshot, error) {
	var snap radarState
	if err := json.Unmarshal(state, &snap); err != nil {
		return MenuSnapshot{}, err
	}
	st := statusOf(state)
	out := MenuSnapshot{Schema: 1, Now: snap.Now, Working: st.working, Waiting: st.waiting, Waits: []MenuSession{}}
	for _, s := range snap.Sessions {
		if !s.Waiting {
			continue
		}
		t := snap.target(&s)
		canJump := t.url != "" || t.editor != "" || t.pid > 0
		if runtime.GOOS == "linux" {
			canJump = t.url != "" || (editorBundles[t.editor] != "" && t.folder != "")
		}
		out.Waits = append(out.Waits, MenuSession{ID: s.ID, Title: t.title, Jumpable: jumpEnabled && canJump})
	}
	return out, nil
}

type radarSession struct {
	Kind      string   `json:"kind"`
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Surface   string   `json:"surface"`
	Cwd       string   `json:"cwd"`
	Folders   []string `json:"folders"`
	Workspace string   `json:"workspace"`
	PID       int      `json:"pid"`
	Model     string   `json:"model"`
	Busy      bool     `json:"busy"`
	Waiting   bool     `json:"waiting"`
	Flows     []struct {
		Sink    string `json:"sink"`
		Model   string `json:"model"`
		AgentID string `json:"agentId"`
		Tokens  int64  `json:"tokens"`
	} `json:"flows"`
}

// target gives the jump target of a session.
func (snap *radarState) target(s *radarSession) jumpTarget {
	t := jumpTo(s.Kind, s.ID, s.Surface, s.Name, s.Cwd, s.Workspace, s.Folders, s.PID)
	if s.Surface == "VS Code agent" {
		// The session belongs to the editor window that has its folder open.
		// The longest folder wins: a window for /repo/sub is closer than a window for /repo.
		best := -1
		for _, ide := range snap.IDEs {
			for _, f := range ide.Folders {
				if ideEditors[ide.IDE] != "" && collector.InFolder(s.Cwd, f) && len(f) > best {
					best, t.editor, t.folder = len(f), ideEditors[ide.IDE], windowPath(ide.Folders, ide.Workspace)
				}
			}
		}
	}
	return t
}

// Jump shows the app of the session with the ID in the JSON state.
// It reports false if the state has no such session, or if the app is unknown.
func Jump(state []byte, id string) bool {
	var snap radarState
	if id == "" || json.Unmarshal(state, &snap) != nil {
		return false
	}
	for i := range snap.Sessions {
		if s := &snap.Sessions[i]; s.ID == id {
			t := snap.target(s)
			return (t.url != "" || t.editor != "" || t.pid > 0) && showTarget(t)
		}
	}
	return false
}

// statusOf gives the counts of active sessions and the models that they use.
// A model is in use when an active session or its sub-agent called it in the last 2 minutes.
// An active session without a recent model call counts for its current model.
func statusOf(state []byte) trayStatus {
	var snap radarState
	var st trayStatus
	if json.Unmarshal(state, &snap) != nil {
		return st
	}
	byKey := map[string]*trayModel{}
	agents := map[string]map[string]bool{}
	byID := map[string]bool{} // the name of the row is a model ID, not a display name
	// use counts the agent for the model. A model call gives the display name, for example "Claude Opus 5.5".
	// The session gives only the model ID, for example "claude-opus-5.5". Both go into one row.
	use := func(name, agent string, tokens int64, call bool) {
		k := modelKey(name)
		m := byKey[k]
		if m == nil {
			m = &trayModel{name: name}
			byKey[k] = m
			agents[k] = map[string]bool{}
			byID[k] = !call
		} else if call && byID[k] {
			m.name, byID[k] = name, false
		}
		m.tokens += tokens
		if !agents[k][agent] {
			agents[k][agent] = true
			m.agents++
		}
	}
	for _, s := range snap.Sessions {
		if s.Waiting {
			st.waiting++
			st.waits = append(st.waits, snap.target(&s))
		} else if s.Busy {
			st.working++
		} else {
			continue
		}
		called := false
		for _, f := range s.Flows {
			if !strings.HasPrefix(f.Sink, "LLM: ") {
				continue
			}
			name := f.Model
			if name == "" {
				name = strings.TrimPrefix(f.Sink, "LLM: ")
			}
			use(name, s.ID+"/"+f.AgentID, f.Tokens, true)
			called = true
		}
		if !called && s.Model != "" {
			use(s.Model, s.ID+"/", 0, false)
		}
	}
	for _, m := range byKey {
		st.models = append(st.models, *m)
	}
	sort.Slice(st.models, func(i, j int) bool {
		a, b := st.models[i], st.models[j]
		if a.tokens != b.tokens {
			return a.tokens > b.tokens
		}
		return a.name < b.name
	})
	return st
}

// modelKey makes the display name and the ID of a model equal.
// "Claude Opus 5.5", "claude-opus-5.5", and "<provider-id>/claude-opus-5.5 · BYOK" all give "claudeopus55".
func modelKey(name string) string {
	name = strings.TrimSuffix(name, " · BYOK")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return name
	}
	return b.String()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
