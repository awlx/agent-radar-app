package tray

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
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

func jumpTo(kind, id, surface, name, cwd string, folders []string, pid int) jumpTarget {
	title := strings.Join(strings.Fields(name), " ")
	if title == "" {
		title = "Session " + id[:min(8, len(id))]
	}
	t := jumpTarget{title: truncate(title, 60) + " — " + surface, folder: cwd, pid: pid}
	if len(folders) > 0 {
		t.folder = folders[0]
	}
	switch {
	case surface == "Copilot app":
		t.url = "ghapp://sessions/" + url.PathEscape(id)
	case kind == "vscode-chat":
		t.editor = strings.TrimSuffix(surface, " chat")
	}
	return t
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

// statusOf gives the counts of active sessions and the models that they use.
// A model is in use when an active session or its sub-agent called it in the last 2 minutes.
// An active session without a recent model call counts for its current model.
func statusOf(state []byte) trayStatus {
	var snap struct {
		Sessions []struct {
			Kind    string   `json:"kind"`
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			Surface string   `json:"surface"`
			Cwd     string   `json:"cwd"`
			Folders []string `json:"folders"`
			PID     int      `json:"pid"`
			Model   string   `json:"model"`
			Busy    bool     `json:"busy"`
			Waiting bool     `json:"waiting"`
			Flows   []struct {
				Sink    string `json:"sink"`
				Model   string `json:"model"`
				AgentID string `json:"agentId"`
				Tokens  int64  `json:"tokens"`
			} `json:"flows"`
		} `json:"sessions"`
		IDEs []struct {
			IDE     string   `json:"ide"`
			Folders []string `json:"folders"`
		} `json:"ides"`
	}
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
			t := jumpTo(s.Kind, s.ID, s.Surface, s.Name, s.Cwd, s.Folders, s.PID)
			if s.Surface == "VS Code agent" {
				// The session belongs to the editor window that has its folder open.
			find:
				for _, ide := range snap.IDEs {
					for _, f := range ide.Folders {
						if ideEditors[ide.IDE] != "" && collector.InFolder(s.Cwd, f) {
							t.editor, t.folder = ideEditors[ide.IDE], f
							break find
						}
					}
				}
			}
			st.waits = append(st.waits, t)
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
