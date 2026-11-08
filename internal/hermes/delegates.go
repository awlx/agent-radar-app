package hermes

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/awlx/agent-radar-app/internal/collector"
)

// delegWindow is the longest time a background delegation shows as running without a completion message.
const delegWindow = 6 * 3600 * 1000

// delegation is one background delegate_task dispatch of a session. Hermes runs the delegates after the
// tool call returns, and it keeps their child sessions out of the session list. The parent messages tell
// the start (the tool result has "status": "dispatched") and the end (Hermes adds a user turn that starts
// with "[ASYNC DELEGATION COMPLETE — <id>]").
type delegation struct {
	id     string
	goals  []string
	models []string // per goal; empty: the default delegation model
	at     int64
	endAt  int64
	failed bool
}

var delegID = regexp.MustCompile(`\bdeleg_[A-Za-z0-9_-]+`)

// delegations finds the background delegations in the messages of a session, oldest first.
func delegations(msgs []message) []*delegation {
	args := map[string]map[string]any{} // delegate_task arguments by tool call ID
	byID := map[string]*delegation{}
	var out []*delegation
	for i := range msgs {
		m := &msgs[i]
		at := int64(m.Timestamp)
		switch m.Role {
		case "assistant":
			for _, tc := range m.calls() {
				if tc.name() == "delegate_task" {
					args[tc.id()] = tc.args()
				}
			}
		case "tool":
			a, ok := args[m.ToolCallID]
			if m.ToolName != "delegate_task" && !ok {
				continue
			}
			d := dispatched(text(m.Content), a)
			if d == nil || byID[d.id] != nil {
				continue
			}
			d.at = at
			byID[d.id] = d
			out = append(out, d)
		case "user":
			t := text(m.Content)
			if !strings.Contains(t, "DELEGATION") {
				continue
			}
			for _, id := range delegID.FindAllString(t, -1) {
				if d := byID[id]; d != nil && d.endAt == 0 {
					d.endAt = max(at, d.at)
					d.failed = strings.Contains(t, "did not complete successfully")
				}
			}
		}
	}
	return out
}

// dispatched reads a delegate_task result. It gives nil if the tasks did not go to the background.
func dispatched(result string, args map[string]any) *delegation {
	if i := strings.IndexByte(result, '{'); i >= 0 {
		result = result[i:]
	}
	var r struct {
		Status       string   `json:"status"`
		DelegationID string   `json:"delegation_id"`
		Goal         string   `json:"goal"`
		Goals        []string `json:"goals"`
		Count        int      `json:"count"`
	}
	if json.NewDecoder(strings.NewReader(result)).Decode(&r) != nil || r.Status != "dispatched" || r.DelegationID == "" {
		return nil
	}
	d := &delegation{id: r.DelegationID, goals: r.Goals}
	if len(d.goals) == 0 && r.Goal != "" {
		d.goals = []string{r.Goal}
	}
	tasks, _ := args["tasks"].([]any)
	if len(d.goals) == 0 {
		for _, t := range tasks {
			if m, ok := t.(map[string]any); ok {
				g, _ := m["goal"].(string)
				d.goals = append(d.goals, g)
			}
		}
	}
	if len(d.goals) == 0 {
		g, _ := args["goal"].(string)
		d.goals = []string{g}
	}
	for len(d.goals) < r.Count {
		d.goals = append(d.goals, "")
	}
	top, _ := args["model"].(string)
	d.models = make([]string, len(d.goals))
	for i := range d.models {
		d.models[i] = top
		if i < len(tasks) {
			if m, ok := tasks[i].(map[string]any); ok {
				if x, _ := m["model"].(string); x != "" {
					d.models[i] = x
				}
			}
		}
	}
	return d
}

func (d *delegation) running(now int64, ended bool) bool {
	return d.endAt == 0 && !ended && now-d.at < delegWindow
}

// delegates gives the radar rows of the delegations. Running ones and ones that ended in the show window.
func delegates(sessionID string, ds []*delegation, model string, ended bool, now int64) ([]collector.Subagent, bool) {
	var out []collector.Subagent
	anyRunning := false
	for _, d := range ds {
		run := d.running(now, ended)
		if !run && (d.endAt == 0 || now-d.endAt > showWindow) {
			continue
		}
		anyRunning = anyRunning || run
		last := d.endAt
		if last == 0 {
			last = d.at
		}
		for i, g := range d.goals {
			m := firstNonEmpty(d.models[i], model)
			name := firstNonEmpty(g, "delegate")
			sub := collector.Subagent{ID: "hermes:" + sessionID + ":" + d.id + ":" + strconv.Itoa(i), Name: collector.Trunc(name, 60),
				Type: "delegate_task", Model: m, StartedAt: d.at, LastAt: last, EndedAt: d.endAt,
				Said: collector.Trunc(g, 400), Running: run, Failed: d.failed}
			if run {
				sub.Doing = "background task"
			}
			out = append(out, sub)
		}
	}
	return out, anyRunning
}

// delegationModel reads the model that Hermes gives to delegates (delegation.model in config.yaml).
// Only the dashboard has the config API. An empty model means: the delegates use the parent model.
func (c *client) delegationModel(ctx context.Context) (string, error) {
	k, err := c.detect(ctx)
	if err != nil || k != kindDashboard {
		return "", err
	}
	var cfg struct {
		Delegation struct {
			Model string `json:"model"`
		} `json:"delegation"`
	}
	if err := c.get(ctx, "/api/config?"+c.query(url.Values{}), &cfg); err != nil {
		return "", err
	}
	return strings.TrimSpace(cfg.Delegation.Model), nil
}
