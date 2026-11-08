package hermes

import (
	"context"
	"log"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/awlx/agent-radar-app/internal/collector"
)

const (
	// Surface is the surface name of Hermes sessions in the radar.
	Surface = "Hermes"

	topLimit      = 30
	subLimit      = 60
	topMessages   = 40
	subMessages   = 20
	maxTopFetch   = 10
	maxSubFetch   = 8
	maxKidSearch  = 4                // searches for new delegate child sessions in one read
	showWindow    = 24 * 3600 * 1000 // sessions older than this do not show
	maxShown      = 20
	recentWindow  = 15 * 60 * 1000 // sessions with activity in this window get their messages
	activeWindow  = 5 * 60 * 1000  // the "is_active" window of Hermes
	toolWindow    = 30 * 60 * 1000 // an open tool call older than this is abandoned
	thinkWindow   = 10 * 60 * 1000 // a user message without an answer in this window: the model thinks
	flowWindow    = 120 * 1000
	staleRun      = 2 * 3600 * 1000 // a cron run without an end after this time is abandoned
	cfgEvery      = 5 * time.Minute
	maxBackoff    = time.Minute
	fetchDeadline = 20 * time.Second
)

// Source reads a Hermes host in the background and gives its sessions to the collector.
type Source struct {
	cfg *Config
	c   *client

	mu   sync.Mutex
	data *data
	err  error

	delegCfg *delegConfig // the delegation config of Hermes; nil: not known. Read again after cfgEvery.
	cfgAt    time.Time
}

type data struct {
	tops []sessionRow
	subs map[string][]sessionRow // by parent session ID
	msgs map[string][]message    // by session ID
	jobs []cronJob

	delegCfg   *delegConfig
	delegating map[string]bool       // session IDs with running background delegates
	kids       map[string]sessionRow // child sessions of delegate tasks, by kidKey
}

// New makes a source. It does not connect until Refresh or Run.
func New(cfg *Config) *Source {
	if cfg.insecure() {
		log.Printf("hermes: WARNING: %s uses http, not https. The credentials go over the network without encryption.", cfg.host)
	}
	return &Source{cfg: cfg, c: newClient(cfg)}
}

// Run refreshes the data each interval until ctx stops. After an error, it waits longer, up to one minute.
func (s *Source) Run(ctx context.Context, interval time.Duration) {
	wait := interval
	for {
		err := s.Refresh(ctx)
		if err != nil {
			wait = min(wait*2, maxBackoff)
		} else {
			wait = interval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Refresh reads the sessions one time. It logs a new error one time.
func (s *Source) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, fetchDeadline)
	defer cancel()
	d, err := s.fetch(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if s.err == nil || s.err.Error() != err.Error() {
			log.Printf("hermes %s: %v", s.cfg.host, err)
		}
		s.err = err
		return err
	}
	if s.err != nil {
		log.Printf("hermes %s: connected", s.cfg.host)
	}
	s.err = nil
	s.data = d
	return nil
}

func (s *Source) fetch(ctx context.Context) (*data, error) {
	now := time.Now().UnixMilli()
	rows, err := s.c.sessions(ctx, topLimit, false)
	if err != nil {
		return nil, err
	}
	subRows, err := s.c.sessions(ctx, subLimit, true)
	if err != nil {
		subRows = nil // older servers: no sub-agent rows
	}
	d := &data{subs: map[string][]sessionRow{}, msgs: map[string][]message{}, delegating: map[string]bool{}, kids: map[string]sessionRow{}}
	d.jobs, _ = s.c.cronJobs(ctx) // older dashboards: no cron API
	if time.Since(s.cfgAt) > cfgEvery {
		if c, err := s.c.delegationConfig(ctx); err == nil {
			s.delegCfg = c
		}
		s.cfgAt = time.Now() // also after an error: do not ask on each poll
	}
	d.delegCfg = s.delegCfg
	for _, r := range rows {
		if r.ID != "" && r.Source != "subagent" && !leftover(r) {
			d.tops = append(d.tops, r)
		}
	}
	for _, r := range subRows {
		if r.ID != "" && r.ParentSession != "" {
			d.subs[r.ParentSession] = append(d.subs[r.ParentSession], r)
		}
	}
	s.mu.Lock()
	var old map[string][]message
	var wasDelegating map[string]bool
	var oldKids map[string]sessionRow
	if s.data != nil {
		old = s.data.msgs
		wasDelegating = s.data.delegating
		oldKids = s.data.kids
	}
	s.mu.Unlock()

	load := func(id string, limit int) {
		m, err := s.c.messages(ctx, id, limit)
		if err != nil {
			if prev, ok := old[id]; ok {
				d.msgs[id] = prev
			}
			return
		}
		d.msgs[id] = m
	}
	sort.SliceStable(d.tops, func(i, j int) bool { return d.tops[i].last() > d.tops[j].last() })
	n := 0
	// A session with running background delegates can be quiet for a long time. Read it first,
	// else the end of its delegates goes unseen.
	for _, r := range d.tops {
		if n < maxTopFetch && wasDelegating[r.ID] && r.EndedAt == 0 {
			load(r.ID, topMessages)
			n++
		}
	}
	for _, r := range d.tops {
		if _, done := d.msgs[r.ID]; done {
			continue
		}
		// The first read also takes open sessions that can have running delegates. Later reads know them.
		first := old == nil && r.EndedAt == 0 && r.Source != "cron" && now-r.last() < delegWindow
		if n < maxTopFetch && (now-r.last() < recentWindow || subRunning(d.subs[r.ID], now) || first) {
			load(r.ID, topMessages)
			n++
		}
	}
	taken := map[string]bool{}
	for _, k := range oldKids {
		taken[k.ID] = true
	}
	n, searches := 0, 0
	for _, r := range d.tops {
		for _, dl := range delegations(d.msgs[r.ID]) {
			if !dl.running(now, r.EndedAt != 0) {
				for i := range dl.tasks { // keep the child of an ended task for its model and tokens
					if k, ok := oldKids[kidKey(r.ID, dl.id, i)]; ok {
						d.kids[kidKey(r.ID, dl.id, i)] = k
					}
				}
				continue
			}
			d.delegating[r.ID] = true
			for i, t := range dl.tasks {
				key := kidKey(r.ID, dl.id, i)
				k, ok := oldKids[key]
				switch {
				case ok && t.endAt == 0 && n < maxSubFetch:
					if fresh, err := s.c.session(ctx, k.ID); err == nil && fresh.ID != "" {
						k = fresh
					}
				case !ok && t.endAt == 0 && searches < maxKidSearch:
					searches++
					k, ok = s.c.findChild(ctx, r.ID, t.goal, dl.at, taken)
				}
				if !ok {
					continue
				}
				d.kids[key], taken[k.ID] = k, true
				if t.endAt == 0 && k.EndedAt == 0 && n < maxSubFetch {
					load(k.ID, subMessages)
					n++
				}
			}
		}
	}
	for _, list := range d.subs {
		for _, r := range list {
			if n < maxSubFetch && r.EndedAt == 0 && now-r.last() < recentWindow {
				load(r.ID, subMessages)
				n++
			}
		}
	}
	return d, nil
}

func subRunning(rows []sessionRow, now int64) bool {
	for _, r := range rows {
		if r.EndedAt == 0 && now-r.last() < activeWindow {
			return true
		}
	}
	return false
}

// leftover tells if a row is an empty cron session that Hermes did not close. It has no title and no messages.
func leftover(r sessionRow) bool {
	return r.Source == "cron" && r.EndedAt == 0 && r.MessageCount == 0 && strings.TrimSpace(r.Title) == ""
}

// Sessions gives the cached sessions in the radar format. It does not wait for the network.
// Busy sessions come first, so that the limit does not remove them.
func (s *Source) Sessions(now int64) []*collector.Session {
	s.mu.Lock()
	d := s.data
	s.mu.Unlock()
	if d == nil {
		return nil
	}
	running := map[string]int64{} // running cron jobs: name -> start
	jobs := map[string]cronJob{}
	for _, j := range d.jobs {
		if start, ok := j.running(now); ok && j.Name != "" {
			running[j.Name] = start
			jobs[j.Name] = j
		}
	}
	var out []*collector.Session
	for _, r := range d.tops {
		if now-r.last() > showWindow && !subRunning(d.subs[r.ID], now) && !d.delegating[r.ID] {
			continue
		}
		ses := s.session(r, d, now)
		if r.Source == "cron" && r.EndedAt == 0 {
			// Hermes names a cron session "<job> · <date>".
			for name := range running {
				if strings.HasPrefix(r.Title, name+" · ") {
					ses.Busy = !ses.Waiting
					delete(running, name)
					break
				}
			}
		}
		out = append(out, ses)
	}
	for name, start := range running {
		out = append(out, s.cronSession(jobs[name], start))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Busy != out[j].Busy {
			return out[i].Busy
		}
		return out[i].LastEventAt > out[j].LastEventAt
	})
	return out[:min(len(out), maxShown)]
}

// cronSession is a running cron job that has no session row yet. Hermes writes the row when the run ends.
func (s *Source) cronSession(j cronJob, start int64) *collector.Session {
	repo := ""
	if j.Workdir != "" {
		repo = path.Base(strings.ReplaceAll(j.Workdir, `\`, "/"))
	}
	return &collector.Session{
		Kind: "hermes", ID: "hermes:" + s.cfg.host + ":cron:" + j.ID, Live: true, Client: "cron", Surface: Surface, Remote: s.cfg.host,
		Name: collector.Trunc("cron · "+j.Name, 90), Cwd: j.Workdir, Repo: repo, Model: j.model(), Busy: true,
		Intent: "Scheduled run", LastEventAt: start,
		RunningTools: []collector.Tool{}, Subagents: []collector.Subagent{}, Flows: []collector.Flow{}, Peers: []collector.Peer{},
		ModelTokens: map[string]*collector.ModelUsage{},
	}
}

// activity is what the messages of a session tell.
type activity struct {
	lastUser, lastAssistant string
	lastAt                  int64
	lastRole                string
	open                    []collector.Tool
	flows                   []collector.Flow
	done                    *collector.Tool // the last tool call with a result
	waiting                 bool
}

func (s *Source) activity(msgs []message, model string, ended bool, now int64, agentID string) activity {
	var a activity
	type pending struct {
		tool collector.Tool
		at   int64
	}
	calls := map[string]*pending{}
	var order []string
	for i := range msgs {
		m := &msgs[i]
		at := int64(m.Timestamp)
		a.lastAt = max(a.lastAt, at)
		if m.Role != "" {
			a.lastRole = m.Role
		}
		switch m.Role {
		case "user":
			if t := text(m.Content); t != "" {
				a.lastUser = collector.Trunc(t, 400)
			}
		case "assistant":
			if t := text(m.Content); t != "" {
				a.lastAssistant = collector.Trunc(t, 400)
			}
			if model != "" && now-at < flowWindow {
				a.flows = append(a.flows, collector.Flow{Sink: "LLM: " + model, Tool: "model", Desc: model, At: at, EndAt: at, OK: true, Model: model, AgentID: agentID})
			}
			for _, tc := range m.calls() {
				name := tc.name()
				if name == "" {
					continue
				}
				args := tc.args()
				desc, raw := describeTool(name, args)
				id := tc.id()
				if id == "" {
					id = name
				}
				calls[id] = &pending{tool: collector.Tool{ID: id, Name: name, Desc: desc, RawDesc: raw, StartedAt: at,
					AgentID: agentID, Sink: s.sink(name, args)}, at: at}
				order = append(order, id)
			}
		case "tool":
			id := m.ToolCallID
			if id == "" {
				id = m.ToolName
			}
			p := calls[id]
			if p == nil {
				continue
			}
			delete(calls, id)
			a.done = &p.tool
			if p.tool.Sink != "" && now-at < flowWindow {
				a.flows = append(a.flows, collector.Flow{Sink: p.tool.Sink, Tool: p.tool.Name, Desc: p.tool.Desc, RawDesc: p.tool.RawDesc, At: p.at, EndAt: at, OK: true, AgentID: agentID})
			}
		}
	}
	if ended {
		return a
	}
	for _, id := range order {
		if p := calls[id]; p != nil && now-p.at < toolWindow {
			a.open = append(a.open, p.tool)
			if p.tool.Name == "clarify" {
				a.waiting = true
			}
			delete(calls, id) // a repeated ID counts one time
		}
	}
	return a
}

func (s *Source) session(r sessionRow, d *data, now int64) *collector.Session {
	ended := r.EndedAt != 0
	a := s.activity(d.msgs[r.ID], r.Model, ended, now, "")
	last := max(r.last(), a.lastAt)

	subs := []collector.Subagent{}
	flows := a.flows
	anySub := false
	for _, c := range d.subs[r.ID] {
		sa := s.activity(d.msgs[c.ID], c.Model, c.EndedAt != 0, now, "hermes:"+c.ID)
		cl := max(c.last(), sa.lastAt)
		running := c.EndedAt == 0 && (now-cl < activeWindow || len(sa.open) > 0)
		name := firstNonEmpty(c.Title, c.Preview, "subagent")
		sub := collector.Subagent{ID: "hermes:" + c.ID, Name: collector.Trunc(name, 60), Type: "delegate_task", Model: c.Model,
			StartedAt: int64(c.StartedAt), LastAt: cl, EndedAt: int64(c.EndedAt), Said: collector.Trunc(firstNonEmpty(c.Preview, sa.lastUser), 400), Running: running}
		if len(sa.open) > 0 {
			t := sa.open[len(sa.open)-1]
			sub.Doing = t.Name
			if t.Desc != "" {
				sub.Doing += ": " + t.Desc
			}
		}
		anySub = anySub || running
		end := sub.EndedAt
		if end == 0 {
			end = cl
		}
		if running || now-end < showWindow {
			subs = append(subs, sub)
		}
		flows = append(flows, sa.flows...)
	}
	model := r.Model
	mt := map[string]*collector.ModelUsage{}
	if model != "" && (r.InputTokens > 0 || r.OutputTokens > 0) {
		mt[model] = &collector.ModelUsage{In: int64(r.InputTokens), Out: int64(r.OutputTokens), Calls: int64(r.APICallCount)}
	}
	kids := map[string]*kid{}
	for key, k := range d.kids {
		if strings.HasPrefix(key, r.ID+":") {
			kids[key] = &kid{row: k, act: s.activity(d.msgs[k.ID], k.Model, k.EndedAt != 0, now, "hermes:"+key)}
		}
	}
	dr := delegates(r.ID, delegations(d.msgs[r.ID]), d.delegCfg, model, ended, now, mt, kids)
	subs = append(subs, dr.subs...)
	flows = append(flows, dr.flows...)
	anySub = anySub || dr.running
	sort.SliceStable(subs, func(i, j int) bool { return subs[i].StartedAt < subs[j].StartedAt })

	recent := now-a.lastAt < thinkWindow
	busy := !ended && !a.waiting && (len(a.open) > 0 || anySub || (recent && (a.lastRole == "user" || a.lastRole == "tool")))
	running := append(a.open, dr.tools...)
	if running == nil {
		running = []collector.Tool{}
	}
	name := firstNonEmpty(r.Title, a.lastUser)
	if r.Source != "cron" { // a cron preview is the system prompt
		name = firstNonEmpty(r.Title, r.Preview, a.lastUser)
	}
	if name == "" && r.Source == "cron" && r.StartedAt > 0 {
		name = "cron · " + time.UnixMilli(int64(r.StartedAt)).Format("Jan 02 15:04")
	}
	if name == "" {
		name = r.ID[:min(8, len(r.ID))]
	}
	repo := ""
	if r.Cwd != "" {
		repo = path.Base(strings.ReplaceAll(r.Cwd, `\`, "/"))
	}
	client := r.Source
	if client == "" {
		client = "unknown"
	}
	if flows == nil {
		flows = []collector.Flow{}
	}
	intent := ""
	if !ended && (busy || a.waiting) {
		intent = collector.Trunc(strings.TrimSpace(r.ActivityDesc), 120)
	}
	return &collector.Session{
		Kind: "hermes", ID: "hermes:" + s.cfg.host + ":" + r.ID, Live: !ended, Client: client, Surface: Surface, Remote: s.cfg.host,
		Name: collector.Trunc(name, 90), Cwd: r.Cwd, Repo: repo, Model: model, Busy: busy, Waiting: a.waiting && !ended,
		Intent: intent, LastEventAt: last, LastUser: a.lastUser, LastAssistant: a.lastAssistant,
		RunningTools: running, Subagents: subs, Flows: flows, Peers: []collector.Peer{},
		Tokens: collector.TokenCount{In: int64(r.InputTokens), Out: int64(r.OutputTokens)}, ModelTokens: mt,
	}
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// sink gives the data destination of a Hermes tool call. Destinations that stay on the Hermes host
// get the host name, so that the radar does not show them as destinations on this computer.
func (s *Source) sink(name string, args map[string]any) string {
	onHost := func(x string) string { return shortHost(s.cfg.host) + ": " + x }
	switch {
	case name == "tool_describe":
		return discoverySink(toolNames(name, args))
	case name == "tool_search":
		return "Tool discovery"
	case name == "tool_call":
		names := toolNames(name, args)
		if len(names) == 0 {
			return onHost("Tools")
		}
		var sinks []string
		seen := map[string]bool{}
		entries, _ := args["calls"].([]any)
		for _, entry := range entries {
			call, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			target, _ := call["name"].(string)
			if target == "" {
				continue
			}
			sink := onHost("Tools")
			if target != "tool_call" && target != "tool_describe" && target != "tool_search" {
				targetArgs, _ := call["arguments"].(map[string]any)
				sink = s.sink(target, targetArgs)
			}
			if sink != "" && !seen[sink] {
				seen[sink] = true
				sinks = append(sinks, sink)
			}
		}
		return strings.Join(sinks, "; ")
	case name == "delegate_task" || name == "todo":
		return ""
	case name == "clarify":
		return "You"
	case name == "terminal" || name == "execute_code" || name == "process":
		if sk := collector.ToolSink("bash", args); sk != "" && sk != "Shell" && sk != "Build / run" && sk != "Containers" && sk != "Git" {
			return sk
		} else if sk != "" {
			return onHost(sk)
		}
		return onHost("Shell")
	case name == "read_file" || name == "write_file" || name == "patch" || name == "search_files":
		return onHost("Filesystem")
	case name == "web_search" || name == "web_extract" || strings.HasPrefix(name, "browser_"):
		return "Web"
	case name == "memory" || name == "session_search":
		return onHost("Memory")
	case name == "send_message":
		return "Messaging"
	case strings.HasPrefix(name, "skill"):
		return onHost("Skills")
	case strings.HasPrefix(name, "mcp_"):
		if srv, _ := mcpTool(name); srv != "" {
			return "MCP: " + srv
		}
		return "MCP: unknown server"
	}
	if sk := collector.ToolSink(name, args); sk != "" {
		if sk == "Filesystem" || sk == "Shell" || sk == "Session DB" {
			return onHost(sk)
		}
		return sk
	}
	return onHost("Tools")
}

// shortHost gives the first label of a DNS name, or an IP address unchanged.
func shortHost(h string) string {
	if strings.Count(h, ".") == 3 && strings.Trim(h, "0123456789.") == "" || strings.Contains(h, ":") {
		return h
	}
	first, _, _ := strings.Cut(h, ".")
	return first
}
