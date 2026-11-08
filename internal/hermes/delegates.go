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

// unknownModel is the model of a delegate when Hermes does not tell it.
const unknownModel = "unknown"

// delegation is one background delegate_task dispatch of a session. Hermes runs the delegates after the
// tool call returns, and it keeps their child sessions out of the session list. The parent messages tell
// the start (the tool result has "status": "dispatched") and the end (Hermes adds a user turn that starts
// with "[ASYNC DELEGATION COMPLETE — <id>]" or "[ASYNC DELEGATION BATCH COMPLETE — <id>]").
type delegation struct {
	id    string
	at    int64
	tasks []*dtask
}

type dtask struct {
	goal   string
	model  string // the model of the call arguments
	option string // the model option of the call arguments
	endAt  int64
	failed bool
	calls  int64
}

// delegConfig is the delegation part of the Hermes config. Hermes gives each delegate:
// the model of the task, else the model of the selected backend option, else delegation.model,
// else the model of the parent session.
type delegConfig struct {
	Model    string `json:"model"`
	Backend  string `json:"backend"`
	Backends map[string]struct {
		Default string `json:"default"`
		Options []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"options"`
	} `json:"backends"`
}

// model gives the model of a task. cfg nil means that the config is not known: then a task without
// its own model has an unknown model.
func (cfg *delegConfig) model(t *dtask, parent string) string {
	if t.model != "" {
		return t.model
	}
	if cfg == nil {
		return unknownModel
	}
	if b, ok := cfg.Backends[cfg.Backend]; ok && len(b.Options) > 0 {
		name := firstNonEmpty(t.option, b.Default)
		for _, o := range b.Options {
			if o.Name == name && o.Model != "" {
				return o.Model
			}
		}
	}
	return firstNonEmpty(cfg.Model, parent, unknownModel)
}

var (
	delegID    = regexp.MustCompile(`\bdeleg_[A-Za-z0-9_-]+`)
	batchTask  = regexp.MustCompile(`(?m)^--- \S+ TASK (\d+)/\d+.*\(status=([a-z_]+)(?:, api_calls=(\d+))?`)
	singleCall = regexp.MustCompile(`API calls: (\d+)`)
)

var doneStatus = map[string]bool{"completed": true, "done": true, "success": true}

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
				if d := byID[id]; d != nil {
					d.complete(t, max(at, d.at))
				}
			}
		}
	}
	return out
}

// complete reads a completion message. A batch can end in parts; each part names its tasks.
func (d *delegation) complete(t string, at int64) {
	parts := batchTask.FindAllStringSubmatch(t, -1)
	if len(parts) == 0 {
		failed := strings.Contains(t, "did not complete successfully") || strings.Contains(t, "interrupted before completing")
		var calls int64
		if m := singleCall.FindStringSubmatch(t); m != nil {
			calls, _ = strconv.ParseInt(m[1], 10, 64)
		}
		for _, k := range d.tasks {
			if k.endAt == 0 {
				k.endAt, k.failed = at, failed
				if len(d.tasks) == 1 {
					k.calls = calls
				}
			}
		}
		return
	}
	for _, p := range parts {
		i, _ := strconv.Atoi(p[1])
		if i < 1 || i > len(d.tasks) || d.tasks[i-1].endAt != 0 {
			continue
		}
		k := d.tasks[i-1]
		k.endAt, k.failed = at, !doneStatus[p[2]]
		k.calls, _ = strconv.ParseInt(p[3], 10, 64)
	}
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
	str := func(m map[string]any, k string) string {
		s, _ := m[k].(string)
		return strings.TrimSpace(s)
	}
	var tasks []map[string]any
	if list, ok := args["tasks"].([]any); ok {
		for _, t := range list {
			m, _ := t.(map[string]any)
			tasks = append(tasks, m)
		}
	}
	goals := r.Goals
	if len(goals) == 0 && r.Goal != "" {
		goals = []string{r.Goal}
	}
	if len(goals) == 0 {
		for _, t := range tasks {
			goals = append(goals, str(t, "goal"))
		}
	}
	if len(goals) == 0 {
		goals = []string{str(args, "goal")}
	}
	for len(goals) < r.Count {
		goals = append(goals, "")
	}
	d := &delegation{id: r.DelegationID}
	for i, g := range goals {
		k := &dtask{goal: g, model: str(args, "model"), option: str(args, "option")}
		if i < len(tasks) && tasks[i] != nil {
			k.model = firstNonEmpty(str(tasks[i], "model"), k.model)
			k.option = firstNonEmpty(str(tasks[i], "option"), k.option)
		}
		d.tasks = append(d.tasks, k)
	}
	return d
}

func (d *delegation) running(now int64, ended bool) bool {
	if ended || now-d.at >= delegWindow {
		return false
	}
	for _, k := range d.tasks {
		if k.endAt == 0 {
			return true
		}
	}
	return false
}

// kid is the child session of a delegate task, and what its messages tell.
type kid struct {
	row sessionRow
	act activity
}

func kidKey(sessionID, delegID string, i int) string {
	return sessionID + ":" + delegID + ":" + strconv.Itoa(i)
}

// delegateRows is the result of delegates.
type delegateRows struct {
	subs    []collector.Subagent
	running bool
	tools   []collector.Tool // the open tool calls of running delegates
	flows   []collector.Flow // the recent tool calls of delegates
}

// delegates gives the radar rows of the delegations: running tasks and tasks that ended in the show window.
// It adds the model use of the tasks to mt, by the model of the task. kids gives the known child sessions.
func delegates(sessionID string, ds []*delegation, cfg *delegConfig, parentModel string, ended bool, now int64, mt map[string]*collector.ModelUsage, kids map[string]*kid) delegateRows {
	var out delegateRows
	for _, d := range ds {
		live := !ended && now-d.at < delegWindow
		for i, k := range d.tasks {
			key := kidKey(sessionID, d.id, i)
			c := kids[key]
			run, endAt := live && k.endAt == 0, k.endAt
			model := cfg.model(k, parentModel)
			calls := k.calls
			if c != nil {
				model = firstNonEmpty(c.row.Model, model)
				calls = max(calls, int64(c.row.APICallCount))
				if run && c.row.EndedAt != 0 { // the child ended; the completion message comes later
					run, endAt = false, int64(c.row.EndedAt)
				}
			}
			if model != unknownModel && mt != nil {
				var in, outTok int64
				if c != nil {
					in, outTok = int64(c.row.InputTokens), int64(c.row.OutputTokens)
				}
				if calls > 0 || in > 0 || outTok > 0 {
					u := mt[model]
					if u == nil {
						u = &collector.ModelUsage{}
						mt[model] = u
					}
					u.Calls += calls
					u.In += in
					u.Out += outTok
				}
			}
			if !run && (endAt == 0 || now-endAt > showWindow) {
				continue
			}
			out.running = out.running || run
			sub := collector.Subagent{ID: "hermes:" + key, Name: collector.Trunc(firstNonEmpty(k.goal, "delegate"), 60),
				Type: "delegate_task", Model: model, StartedAt: d.at, LastAt: max(endAt, d.at), EndedAt: endAt,
				Said: collector.Trunc(k.goal, 400), Running: run, Failed: k.failed}
			if run {
				sub.Doing = "background task"
			}
			if c != nil {
				sub.LastAt = max(sub.LastAt, c.act.lastAt)
				switch {
				case run && len(c.act.open) > 0:
					sub.Doing = toolLabel(c.act.open[len(c.act.open)-1])
					out.tools = append(out.tools, c.act.open...)
				case run && c.act.done != nil:
					sub.Doing = "thinking · last: " + toolLabel(*c.act.done)
				case run:
					sub.Doing = "thinking"
				}
				out.flows = append(out.flows, c.act.flows...)
			}
			out.subs = append(out.subs, sub)
		}
	}
	return out
}

func toolLabel(t collector.Tool) string {
	if t.Desc != "" {
		return t.Name + ": " + t.Desc
	}
	return t.Name
}

var goalWord = regexp.MustCompile(`[\p{L}\p{N}]+`)

// findChild finds the child session of a delegate task. Hermes keeps child sessions out of the session
// list, but its search finds them by the goal text. The child must have the parent and start after the
// dispatch. taken has the child IDs that other tasks use.
func (c *client) findChild(ctx context.Context, parent, goal string, at int64, taken map[string]bool) (sessionRow, bool) {
	w := goalWord.FindAllString(goal, 8)
	if len(w) == 0 {
		return sessionRow{}, false
	}
	if k, err := c.detect(ctx); err != nil || k != kindDashboard {
		return sessionRow{}, false
	}
	v := url.Values{"q": {`"` + strings.Join(w, " ") + `"`}, "source": {"subagent"}, "limit": {"20"}}
	var out struct {
		Results []sessionRow `json:"results"`
	}
	if c.get(ctx, "/api/sessions/search?"+c.query(v), &out) != nil {
		return sessionRow{}, false
	}
	// Prefer a child whose first message (the preview) starts like the goal; two goals can share 8 words.
	norm := func(s string) string { return strings.Join(goalWord.FindAllString(strings.ToLower(s), -1), " ") }
	g := norm(goal)
	like := func(r sessionRow) bool {
		p := norm(r.Preview)
		return p != "" && (strings.HasPrefix(g, p) || strings.HasPrefix(p, g))
	}
	var best sessionRow
	for _, r := range out.Results {
		if r.ID == "" || r.ParentSession != parent || taken[r.ID] || int64(r.StartedAt) < at-60*1000 {
			continue
		}
		if best.ID == "" || (like(r) && !like(best)) || (like(r) == like(best) && r.StartedAt < best.StartedAt) {
			best = r
		}
	}
	return best, best.ID != ""
}

// session reads one session row.
func (c *client) session(ctx context.Context, id string) (sessionRow, error) {
	var r sessionRow
	err := c.get(ctx, "/api/sessions/"+url.PathEscape(id)+"?"+c.query(url.Values{}), &r)
	return r, err
}

// delegationConfig reads the delegation part of the Hermes config. Only the dashboard has the config API.
// The radar keeps only the model names; it does not keep other config values.
func (c *client) delegationConfig(ctx context.Context) (*delegConfig, error) {
	k, err := c.detect(ctx)
	if err != nil {
		return nil, err
	}
	if k != kindDashboard {
		return nil, nil
	}
	var cfg struct {
		Delegation *delegConfig `json:"delegation"`
	}
	if err := c.get(ctx, "/api/config?"+c.query(url.Values{}), &cfg); err != nil {
		return nil, err
	}
	if cfg.Delegation == nil {
		return &delegConfig{}, nil
	}
	return cfg.Delegation, nil
}
