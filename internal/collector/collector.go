package collector

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	recentMS     = 15 * 60 * 1000
	flowWindowMS = 120 * 1000
	// WaitingWindow is how long a session can wait for the user (a permission or an ask_user question).
	WaitingWindow = 6 * 3600 * 1000
	initialTail   = 8 << 20
	maxReadChunk  = 32 << 20
	maxFlows      = 200
	maxLLM        = 5000
	maxPeers      = 50
	// A process can write its lock file some seconds after its first events.
	lockSlackMS = 30 * 1000
)

// ---- Snapshot types (JSON shape is what ui/ui.html expects) ----

type Snapshot struct {
	Now      int64      `json:"now"`
	Sessions []*Session `json:"sessions"`
	IDEs     []IDE      `json:"ides"`
}

type Session struct {
	Kind          string                 `json:"kind"`
	ID            string                 `json:"id"`
	Live          bool                   `json:"live"`
	Client        string                 `json:"client,omitempty"`
	Surface       string                 `json:"surface"`
	Name          string                 `json:"name"`
	Cwd           string                 `json:"cwd"`
	Folders       []string               `json:"folders,omitempty"`
	Repo          string                 `json:"repo"`
	Model         string                 `json:"model"`
	Busy          bool                   `json:"busy"`
	Waiting       bool                   `json:"waiting"`
	PID           int                    `json:"pid,omitempty"` // a live process that has the session open
	LastEventAt   int64                  `json:"lastEventAt"`
	LastUser      string                 `json:"lastUser"`
	LastAssistant string                 `json:"lastAssistant"`
	Intent        string                 `json:"intent"`
	RunningTools  []Tool                 `json:"runningTools"`
	Subagents     []Subagent             `json:"subagents"`
	Flows         []Flow                 `json:"flows"`
	Peers         []Peer                 `json:"peers"`
	Tokens        TokenCount             `json:"tokens"`
	ModelTokens   map[string]*ModelUsage `json:"modelTokens"`
}

type Tool struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Desc      string `json:"desc"`
	StartedAt int64  `json:"startedAt"`
	AgentID   string `json:"agentId,omitempty"`
	Sink      string `json:"sink,omitempty"`
}

type Flow struct {
	Sink    string `json:"sink"`
	Tool    string `json:"tool"`
	Desc    string `json:"desc"`
	At      int64  `json:"at"`
	EndAt   int64  `json:"endAt"`
	OK      bool   `json:"ok"`
	AgentID string `json:"agentId,omitempty"`
	Model   string `json:"model,omitempty"`
	Tin     int64  `json:"tin,omitempty"`
	Tout    int64  `json:"tout,omitempty"`
	Tokens  int64  `json:"tokens,omitempty"`
	// Observation marks a token total that grew. VS Code does not record model calls, so an
	// observation has calls: null (unknown). A flow without the field is one model call.
	Observation  bool `json:"observation,omitempty"`
	CallsUnknown bool `json:"-"`
}

// unknownCalls adds "calls": null to a JSON object.
type unknownCalls struct {
	Calls *int64 `json:"calls"`
}

func (f Flow) MarshalJSON() ([]byte, error) {
	type plain Flow
	if !f.CallsUnknown {
		return json.Marshal(plain(f))
	}
	return json.Marshal(struct {
		plain
		unknownCalls
	}{plain(f), unknownCalls{}})
}

type Subagent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Model     string `json:"model"`
	Mode      string `json:"mode,omitempty"`
	StartedAt int64  `json:"startedAt"`
	LastAt    int64  `json:"lastAt"`
	EndedAt   int64  `json:"endedAt,omitempty"`
	Said      string `json:"said"`
	Running   bool   `json:"running"`
	Failed    bool   `json:"failed,omitempty"`
	Doing     string `json:"doing,omitempty"`
}

type Peer struct {
	To   string `json:"to"`
	At   int64  `json:"at"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type TokenCount struct {
	In  int64 `json:"in"`
	Out int64 `json:"out"`
}

type ModelUsage struct {
	In    int64 `json:"in"`
	Out   int64 `json:"out"`
	Calls int64 `json:"calls"`
	// CallsUnknown sends calls: null. VS Code does not record model calls.
	CallsUnknown bool `json:"-"`
}

func (m ModelUsage) MarshalJSON() ([]byte, error) {
	type plain ModelUsage
	if !m.CallsUnknown {
		return json.Marshal(plain(m))
	}
	return json.Marshal(struct {
		In    int64  `json:"in"`
		Out   int64  `json:"out"`
		Calls *int64 `json:"calls"`
	}{m.In, m.Out, nil})
}

// count gives a token or call count from a log. Values that are not finite non-negative numbers count as 0.
func count(v any) int64 {
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > math.MaxInt64/2 {
		return 0
	}
	return int64(f)
}

type IDE struct {
	ID      string   `json:"id"`
	IDE     string   `json:"ide"`
	PID     int      `json:"pid"`
	Folders []string `json:"folders"`
}

// ---- Collector ----

type Collector struct {
	copilotHome string
	stateDir    string
	ideDir      string
	vscodeDirs  []string

	mu       sync.Mutex
	sessions map[string]*sessionEntry
	vscode   map[string]*vscodeEntry

	vscodeScanStart int64
}

func New() *Collector {
	home, _ := os.UserHomeDir()
	ch := os.Getenv("COPILOT_HOME")
	if ch == "" {
		ch = filepath.Join(home, ".copilot")
	}
	return &Collector{
		copilotHome: ch,
		stateDir:    filepath.Join(ch, "session-state"),
		ideDir:      filepath.Join(ch, "ide"),
		vscodeDirs:  vscodeUserDirs(home),
		sessions:    map[string]*sessionEntry{},
		vscode:      map[string]*vscodeEntry{},
	}
}

func (c *Collector) Snapshot() *Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UnixMilli()
	sessions := c.collectCopilotSessions(now)
	sessions = append(sessions, c.collectVSCodeChats(now)...)
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].Busy != sessions[j].Busy {
			return sessions[i].Busy
		}
		return sessions[i].LastEventAt > sessions[j].LastEventAt
	})
	if sessions == nil {
		sessions = []*Session{}
	}
	return &Snapshot{Now: now, Sessions: sessions, IDEs: c.collectIDEs()}
}

// ---- Copilot (app / CLI / VS Code agent-host) sessions ----
// Parsed incrementally: each poll reads only the bytes appended since the last poll.

type toolStart struct {
	Tool
	carriedAt int64
}

type parseState struct {
	model, lastUser, lastAssistant, intent string
	lastEventAt                            int64
	toolStarts                             map[string]*toolStart
	subs                                   map[string]*Subagent
	subOrder                               []string
	turnOpen                               bool
	perms                                  *pendingSet
	inputs                                 *pendingSet
	seq                                    int
	flows                                  []Flow
	llm                                    []Flow
	peers                                  []Peer
	tokens                                 TokenCount
	modelTokens                            map[string]*ModelUsage
	mcpServers                             map[string]bool
}

func newParseState() *parseState {
	return &parseState{toolStarts: map[string]*toolStart{}, subs: map[string]*Subagent{}, modelTokens: map[string]*ModelUsage{},
		mcpServers: map[string]bool{}, perms: newPendingSet(), inputs: newPendingSet()}
}

// pendingSet holds open requests (permissions or questions) and their times, in request order.
type pendingSet struct {
	keys []string
	at   map[string]int64
}

func newPendingSet() *pendingSet { return &pendingSet{at: map[string]int64{}} }

func (m *pendingSet) set(k string, ts int64) {
	if _, ok := m.at[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.at[k] = ts
}

func (m *pendingSet) delete(k string) bool {
	if _, ok := m.at[k]; !ok {
		return false
	}
	delete(m.at, k)
	for i, x := range m.keys {
		if x == k {
			m.keys = append(m.keys[:i:i], m.keys[i+1:]...)
			break
		}
	}
	return true
}

// end removes a completed request. Without an ID, it removes the oldest open request.
func (m *pendingSet) end(id any) {
	if id != nil {
		m.delete(str(id))
		return
	}
	if len(m.keys) > 0 {
		m.delete(m.keys[0])
	}
}

// open tells if a request is open that current accepts.
func (m *pendingSet) open(current func(int64) bool) bool {
	for _, k := range m.keys {
		if current(m.at[k]) {
			return true
		}
	}
	return false
}

// clearPending stops the open work of the session: tools, questions, and sub-agents that still run.
func (p *parseState) clearPending(ts int64) {
	p.toolStarts = map[string]*toolStart{}
	p.perms = newPendingSet()
	p.inputs = newPendingSet()
	for _, a := range p.subs {
		if a.Running {
			a.Running = false
			a.EndedAt = ts
		}
	}
}

func (p *parseState) requestKey(id any) string {
	if id != nil {
		return str(id)
	}
	k := "#" + strconv.Itoa(p.seq)
	p.seq++
	return k
}

type sessionEntry struct {
	p         *parseState
	offset    int64
	ino       uint64
	partial   []byte
	skipFirst bool
	ws        map[string]string
	wsMtime   time.Time
}

type rawEvent struct {
	Type      string    `json:"type"`
	AgentID   string    `json:"agentId"`
	Timestamp string    `json:"timestamp"`
	Data      eventData `json:"data"`
}

type eventData struct {
	Content          any             `json:"content"`
	Model            string          `json:"model"`
	SelectedModel    string          `json:"selectedModel"`
	NewModel         string          `json:"newModel"`
	ToolName         string          `json:"toolName"`
	ToolCallID       string          `json:"toolCallId"`
	ParentToolCallID string          `json:"parentToolCallId"`
	Arguments        json.RawMessage `json:"arguments"`
	Success          *bool           `json:"success"`
	Result           json.RawMessage `json:"result"`
	AgentDisplayName string          `json:"agentDisplayName"`
	AgentName        string          `json:"agentName"`
	AgentType        string          `json:"agentType"`
	ExecutionMode    string          `json:"executionMode"`
	MCPServerName    string          `json:"mcpServerName"`
	ToolRequests     json.RawMessage `json:"toolRequests"`
	AlreadyInUse     *bool           `json:"alreadyInUse"`
	SessionWasActive bool            `json:"sessionWasActive"`
	ContinuePending  bool            `json:"continuePendingWork"`
	RequestID        any             `json:"requestId"`
	Usage            *struct {
		Model            string `json:"model"`
		ModelDisplayName string `json:"modelDisplayName"`
		InputTokens      any    `json:"inputTokens"`
		OutputTokens     any    `json:"outputTokens"`
		Duration         any    `json:"duration"`
		IsByok           bool   `json:"isByok"`
	} `json:"usage"`
}

var (
	reUUID          = regexp.MustCompile(`(?i)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
	reByokPrefix    = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f-]{27}/`)
	reSessionID     = regexp.MustCompile(`^[0-9a-f-]{36}$`)
	reInuseLock     = regexp.MustCompile(`^inuse\.(\d+)\.lock$`)
	reSpawnTool     = regexp.MustCompile(`^(create_session|open_pr_session|open_issue_session)$`)
	reCLI           = regexp.MustCompile(`(?i)cli`)
	typeFieldPrefix = []byte(`"type":"`)
)

// Only these event types change state. Other lines are skipped before JSON decode.
var relevantTypes = set("user.message", "assistant.turn_start", "assistant.turn_end", "session.idle",
	"session.task_complete", "abort", "session.shutdown", "assistant.message", "tool.execution_start",
	"tool.execution_complete", "subagent.started", "subagent.completed", "subagent.failed", "permission.requested",
	"permission.completed", "elicitation.requested", "elicitation.completed", "user_input.requested",
	"user_input.completed", "session.usage_record", "session.start", "session.resume", "session.model_change")

func eventType(line []byte) string {
	i := bytes.Index(line, typeFieldPrefix)
	if i < 0 || i > 64 {
		return ""
	}
	rest := line[i+len(typeFieldPrefix):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}

func parseTS(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func (p *parseState) apply(e *rawEvent) {
	d := &e.Data
	ts := parseTS(e.Timestamp)
	if ts > p.lastEventAt {
		p.lastEventAt = ts
	}
	agentID := e.AgentID
	if agentID == "" {
		agentID = d.ParentToolCallID
	}
	sa := p.subs[agentID]
	if sa != nil && ts > sa.LastAt {
		sa.LastAt = ts
	}
	top := agentID == ""
	switch e.Type {
	case "user.message":
		if top {
			p.lastUser = trunc(str(d.Content), 200)
			p.turnOpen = true
			p.intent = ""
		}
	case "assistant.turn_start":
		if top {
			p.turnOpen = true
		}
	case "session.start", "session.resume":
		if top && d.SelectedModel != "" {
			p.model = d.SelectedModel
		}
		if e.Type == "session.resume" && top {
			if d.ContinuePending {
				// The new process continues the pending work of the old process. Mark it, so that the
				// lock-age filter keeps it.
				for _, t := range p.toolStarts {
					t.carriedAt = ts
				}
				for _, m := range []*pendingSet{p.perms, p.inputs} {
					for k := range m.at {
						m.at[k] = ts
					}
				}
				for _, a := range p.subs {
					if a.Running && ts > a.LastAt {
						a.LastAt = ts
					}
				}
			} else if d.AlreadyInUse != nil && !*d.AlreadyInUse && !d.SessionWasActive {
				// A new process opened a session that no process held and did not continue its work.
				p.clearPending(ts)
				p.turnOpen = false
			}
		}
	case "session.model_change":
		if top && d.NewModel != "" {
			p.model = d.NewModel
		}
	// session.idle is not written to the log, and session.task_complete is optional.
	case "assistant.turn_end":
		if top {
			p.turnOpen = false
		}
	case "session.idle", "session.task_complete", "abort", "session.shutdown":
		if top {
			p.turnOpen = false
			if e.Type != "session.task_complete" {
				p.clearPending(ts)
			}
		}
	case "assistant.message":
		content := str(d.Content)
		if top && d.Model != "" {
			p.model = d.Model
		}
		if top && content != "" {
			p.lastAssistant = trunc(content, 240)
		}
		if sa != nil && content != "" {
			sa.Said = trunc(content, 160)
		}
		var reqs []any
		_ = json.Unmarshal(d.ToolRequests, &reqs)
		for _, r := range reqs {
			if m, ok := r.(map[string]any); ok {
				if srv, ok := m["mcpServerName"].(string); ok && srv != "" {
					p.mcpServers[srv] = true
				}
			}
		}
	case "tool.execution_start":
		var args map[string]any
		_ = json.Unmarshal(d.Arguments, &args)
		if top && d.Model != "" {
			p.model = d.Model
		}
		if d.MCPServerName != "" {
			p.mcpServers[d.MCPServerName] = true
		}
		p.toolStarts[d.ToolCallID] = &toolStart{Tool: Tool{
			ID: d.ToolCallID, Name: d.ToolName, Desc: describeArgs(args), StartedAt: ts, AgentID: agentID,
			Sink: classifyTool(d.ToolName, args, d.MCPServerName, p.mcpServers),
		}}
		if d.ToolName == "report_intent" && top {
			p.intent = trunc(str(args["intent"]), 80)
		}
		if d.ToolName == "send_session_message" {
			if sid := str(args["session_id"]); sid != "" {
				p.peers = append(p.peers, Peer{To: sid, At: ts, Kind: "message", Text: trunc(str(args["message"]), 160)})
			}
		}
	case "tool.execution_complete":
		if top && d.Model != "" {
			p.model = d.Model
		}
		t := p.toolStarts[d.ToolCallID]
		if t == nil {
			break
		}
		delete(p.toolStarts, d.ToolCallID)
		if t.Sink != "" {
			p.flows = append(p.flows, Flow{Sink: t.Sink, Tool: t.Name, Desc: t.Desc, At: t.StartedAt, EndAt: ts,
				OK: d.Success == nil || *d.Success, AgentID: t.AgentID})
		}
		if reSpawnTool.MatchString(t.Name) {
			if m := reUUID.FindString(string(d.Result)); m != "" {
				p.peers = append(p.peers, Peer{To: m, At: ts, Kind: "spawn", Text: t.Desc})
			}
		}
	case "subagent.started":
		id := e.AgentID
		if id == "" {
			id = d.ToolCallID
		}
		name := d.AgentDisplayName
		if name == "" {
			name = d.AgentName
		}
		typ := d.AgentType
		if typ == "" {
			typ = d.AgentName
		}
		if _, ok := p.subs[id]; !ok {
			p.subOrder = append(p.subOrder, id)
		}
		p.subs[id] = &Subagent{ID: id, Name: name, Type: typ, Model: d.Model, StartedAt: ts, LastAt: ts, Mode: d.ExecutionMode, Running: true}
	case "subagent.completed", "subagent.failed":
		id := e.AgentID
		if id == "" {
			id = d.ToolCallID
		}
		if s := p.subs[id]; s != nil {
			s.Running = false
			s.EndedAt = ts
			s.Failed = e.Type == "subagent.failed"
		}
		for k, t := range p.toolStarts {
			if t.AgentID == id {
				delete(p.toolStarts, k)
			}
		}
	case "permission.requested":
		p.perms.set(p.requestKey(d.RequestID), ts)
	case "permission.completed":
		p.perms.end(d.RequestID)
	case "elicitation.requested", "user_input.requested":
		p.inputs.set(p.requestKey(d.RequestID), ts)
	case "elicitation.completed", "user_input.completed":
		p.inputs.end(d.RequestID)
	case "session.usage_record":
		u := d.Usage
		if u == nil {
			break
		}
		raw := u.ModelDisplayName
		if raw == "" {
			raw = u.Model
		}
		if raw == "" {
			raw = "model"
		}
		name := reByokPrefix.ReplaceAllString(raw, "")
		if u.IsByok {
			name += " · BYOK"
		}
		tin, tout := count(u.InputTokens), count(u.OutputTokens)
		p.llm = append(p.llm, Flow{Sink: "LLM: " + name, Tool: "model", Model: name, Tin: tin, Tout: tout,
			Desc: fmt.Sprintf("%d in / %d out tokens", tin, tout), At: ts - count(u.Duration), EndAt: ts, OK: true,
			AgentID: agentID, Tokens: tin + tout})
		p.tokens.In += tin
		p.tokens.Out += tout
		mt := p.modelTokens[name]
		if mt == nil {
			mt = &ModelUsage{}
			p.modelTokens[name] = mt
		}
		mt.In += tin
		mt.Out += tout
		mt.Calls++
	}
	if len(p.flows) > maxFlows {
		p.flows = append([]Flow(nil), p.flows[len(p.flows)-maxFlows:]...)
	}
	// Model calls are kept by time, not by count, so that the 2-minute token rates stay correct.
	for len(p.llm) > 0 && p.llm[0].EndAt < p.lastEventAt-flowWindowMS {
		p.llm = p.llm[1:]
	}
	if len(p.llm) > maxLLM {
		p.llm = append([]Flow(nil), p.llm[len(p.llm)-maxLLM:]...)
	}
	if len(p.peers) > maxPeers {
		p.peers = append([]Peer(nil), p.peers[len(p.peers)-maxPeers:]...)
	}
}

// Only lines that contain a type name or a JSON escape (the type name can be escaped) are parsed.
var prescanMarks = [][]byte{
	[]byte("subagent."), []byte("session.start"), []byte("session.resume"), []byte("session.model_change"), []byte(`\u`),
}

func prescanLine(line []byte) bool {
	for _, x := range prescanMarks {
		if bytes.Contains(line, x) {
			return true
		}
	}
	return false
}

func prescanType(t string) bool {
	return t == "session.start" || t == "session.resume" || t == "session.model_change" || strings.HasPrefix(t, "subagent.")
}

// prescanSubagents applies only the sub-agent lifecycle and model lines in [0, end).
// The initial tail can start after a sub-agent started or after the model was selected.
// Without this scan, tools in the tail point to an agent ID that has no name, and the
// session does not show its current model. It returns the end of the last complete line
// in [0, end), where the tail reader must start.
func prescanSubagents(file string, p *parseState, end int64) (int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.LimitReader(f, end), 1<<20)
	var pos, boundary int64
	for {
		line, err := r.ReadSlice('\n')
		pos += int64(len(line))
		if err == bufio.ErrBufferFull {
			// Long line: never a lifecycle or model event. Skip the remainder.
			for err == bufio.ErrBufferFull {
				line, err = r.ReadSlice('\n')
				pos += int64(len(line))
			}
			if err != nil {
				break
			}
			boundary = pos
			continue
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			boundary = pos
			if prescanLine(line) {
				var ev rawEvent
				if json.Unmarshal(line, &ev) == nil && prescanType(ev.Type) {
					p.apply(&ev)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return boundary, nil
}

func (e *sessionEntry) readAppended(file string, size int64, ino uint64) error {
	if e.p == nil || ino != e.ino || size < e.offset {
		e.p = newParseState()
		e.ino = ino
		e.partial = nil
		e.offset = max(0, size-initialTail)
		e.skipFirst = false
		if e.offset > 0 {
			boundary, err := prescanSubagents(file, e.p, e.offset)
			if err != nil {
				return err
			}
			// Start the tail at a full line, so that the line across the boundary is not lost.
			// A very long line there is never a lifecycle event: skip it, as before.
			if e.offset-boundary <= 1<<20 {
				e.offset = boundary
			} else {
				e.skipFirst = true
			}
		}
	}
	if size == e.offset {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	n := min(size-e.offset, maxReadChunk)
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, e.offset); err != nil && err != io.EOF {
		return err
	}
	e.offset += n
	text := append(e.partial, buf...)
	if e.skipFirst {
		if i := bytes.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		} else {
			text = nil
		}
		e.skipFirst = false
	}
	nl := bytes.LastIndexByte(text, '\n')
	e.partial = append([]byte(nil), text[nl+1:]...)
	if nl < 0 {
		return nil
	}
	sc := bufio.NewScanner(bytes.NewReader(text[:nl+1]))
	sc.Buffer(make([]byte, 1<<20), maxReadChunk)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' || !relevantTypes[eventType(line)] {
			continue
		}
		var ev rawEvent
		if json.Unmarshal(line, &ev) == nil {
			e.p.apply(&ev)
		}
	}
	return nil
}

func (c *Collector) collectCopilotSession(id string, now int64) *Session {
	dir := filepath.Join(c.stateDir, id)
	eventsFile := filepath.Join(dir, "events.jsonl")
	st, err := os.Stat(eventsFile)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	// A process makes its lock file when it opens the session. Pending work from before the
	// oldest live lock belongs to a process that stopped (crash, then resume).
	live, since, ownerPID, ownerBirth := false, int64(math.MaxInt64), 0, int64(0)
	for _, de := range entries {
		if m := reInuseLock.FindStringSubmatch(de.Name()); m != nil {
			if pid, _ := strconv.Atoi(m[1]); pidAlive(pid) {
				live = true
				if ls, err := os.Stat(filepath.Join(dir, de.Name())); err == nil {
					since = min(since, fileBirth(ls))
					if b := fileBirth(ls); ownerPID == 0 || b > ownerBirth {
						ownerPID, ownerBirth = pid, b
					}
				}
			}
		}
	}
	current := func(t int64) bool { return !(t < since-lockSlackMS) }
	if !live && now-st.ModTime().UnixMilli() > recentMS {
		delete(c.sessions, id)
		return nil
	}

	e := c.sessions[id]
	if e == nil {
		e = &sessionEntry{}
		c.sessions[id] = e
	}
	wsPath := filepath.Join(dir, "workspace.yaml")
	if wst, err := os.Stat(wsPath); err == nil {
		if !wst.ModTime().Equal(e.wsMtime) {
			if b, err := os.ReadFile(wsPath); err == nil {
				e.ws = parseYAML(string(b))
				e.wsMtime = wst.ModTime()
			}
		}
	}
	if e.ws == nil {
		e.ws = map[string]string{}
	}
	if err := e.readAppended(eventsFile, st.Size(), fileIno(st)); err != nil || e.p == nil {
		return nil
	}

	p, ws := e.p, e.ws
	quiet := now - p.lastEventAt
	client := ws["client_name"]
	if client == "" {
		client = "unknown"
	}

	subagents := []Subagent{}
	for _, sid := range p.subOrder {
		a := *p.subs[sid]
		a.Running = live && a.Running && current(a.LastAt) && now-a.LastAt < 20*60e3
		end := a.EndedAt
		if end == 0 {
			end = a.LastAt
		}
		if a.Running || now-end < 5*60e3 {
			subagents = append(subagents, a)
		}
	}
	runningTools := []Tool{}
	for _, t := range p.toolStarts {
		// Ignore orphaned tool starts (crashed or abandoned turns). A question to the user stays
		// pending for the full waiting window, because it can wait a long time for an answer.
		keep := now-t.StartedAt < 30*60e3 || quiet < 10*60e3
		if t.Name == "ask_user" {
			keep = quiet < WaitingWindow
		}
		at := t.StartedAt
		if t.carriedAt != 0 {
			at = t.carriedAt
		}
		if live && current(at) && keep {
			runningTools = append(runningTools, t.Tool)
		}
	}
	sort.Slice(runningTools, func(i, j int) bool { return runningTools[i].StartedAt < runningTools[j].StartedAt })
	waitingTool, anySubRunning := false, false
	for _, t := range runningTools {
		if t.Name == "ask_user" {
			waitingTool = true
		}
		for i := range subagents {
			if t.AgentID != "" && subagents[i].ID == t.AgentID {
				if t.Desc != "" {
					subagents[i].Doing = t.Name + ": " + t.Desc
				} else {
					subagents[i].Doing = t.Name
				}
			}
		}
	}
	for _, a := range subagents {
		anySubRunning = anySubRunning || a.Running
	}
	waiting := live && (p.perms.open(current) || p.inputs.open(current) || waitingTool) && quiet < WaitingWindow
	busy := live && !waiting && (len(runningTools) > 0 || anySubRunning || (p.turnOpen && quiet < 3*60e3))

	surface := client
	switch {
	case client == "vscode-agent-host":
		surface = "VS Code agent"
	case client == "github/autopilot":
		surface = "Copilot app"
	case reCLI.MatchString(client):
		surface = "Copilot CLI"
	}
	name := ws["name"]
	if name == "" {
		name = ws["summary"]
	}
	if name == "" {
		name = p.lastUser
	}
	if name == "" {
		name = id[:8]
	}
	flows := []Flow{}
	for _, list := range [][]Flow{p.flows, p.llm} {
		for _, f := range list {
			if now-f.EndAt < flowWindowMS {
				flows = append(flows, f)
			}
		}
	}
	peers := p.peers
	if len(peers) > 30 {
		peers = peers[len(peers)-30:]
	}
	mt := make(map[string]*ModelUsage, len(p.modelTokens))
	for k, v := range p.modelTokens {
		cp := *v
		mt[k] = &cp
	}
	cwd := ws["cwd"]
	repo := ""
	if cwd != "" {
		repo = filepath.Base(cwd)
	}
	return &Session{
		Kind: "copilot", ID: id, Live: live, Client: client, Surface: surface,
		Name: trunc(name, 90), Cwd: cwd, Repo: repo, Model: p.model, Busy: busy, Waiting: waiting,
		PID: ownerPID, LastEventAt: p.lastEventAt, LastUser: p.lastUser, LastAssistant: p.lastAssistant, Intent: p.intent,
		RunningTools: runningTools, Subagents: subagents, Flows: flows, Peers: append([]Peer{}, peers...),
		Tokens: p.tokens, ModelTokens: mt,
	}
}

func (c *Collector) collectCopilotSessions(now int64) []*Session {
	ents, err := os.ReadDir(c.stateDir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []*Session
	for _, de := range ents {
		id := de.Name()
		if !reSessionID.MatchString(id) {
			continue
		}
		seen[id] = true
		if s := c.collectCopilotSession(id, now); s != nil {
			out = append(out, s)
		}
	}
	for id := range c.sessions {
		if !seen[id] {
			delete(c.sessions, id)
		}
	}
	return out
}

var reYAMLKey = regexp.MustCompile(`^([A-Za-z_]+):\s*(.*)$`)
var reYAMLBlock = regexp.MustCompile(`^[|>][-+]?$`)

func parseYAML(text string) map[string]string {
	o := map[string]string{}
	block := ""
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if block != "" && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if o[block] != "" {
				o[block] += " "
			}
			o[block] += strings.TrimSpace(line)
			continue
		}
		block = ""
		m := reYAMLKey.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		if reYAMLBlock.MatchString(m[2]) {
			block = m[1]
			o[m[1]] = ""
		} else {
			v := m[2]
			if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
				v = v[1 : len(v)-1]
			}
			o[m[1]] = v
		}
	}
	return o
}

// ---- VS Code windows bridged to Copilot ----

func (c *Collector) collectIDEs() []IDE {
	out := []IDE{}
	ents, err := os.ReadDir(c.ideDir)
	if err != nil {
		return out
	}
	for _, de := range ents {
		if !strings.HasSuffix(de.Name(), ".lock") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.ideDir, de.Name()))
		if err != nil {
			continue
		}
		var j struct {
			PID              int      `json:"pid"`
			IDEName          string   `json:"ideName"`
			WorkspaceFolders []string `json:"workspaceFolders"`
		}
		if json.Unmarshal(b, &j) != nil || j.PID == 0 || !pidAlive(j.PID) {
			continue
		}
		if j.IDEName == "" {
			j.IDEName = "IDE"
		}
		if j.WorkspaceFolders == nil {
			j.WorkspaceFolders = []string{}
		}
		out = append(out, IDE{ID: strings.TrimSuffix(de.Name(), ".lock"), IDE: j.IDEName, PID: j.PID, Folders: j.WorkspaceFolders})
	}
	return out
}
