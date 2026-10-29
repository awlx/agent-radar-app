package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// ---- VS Code Copilot Chat panel sessions ----
// A chat file is a mutation log: {kind:0,v} sets the full state, {kind:1,k,v} sets the value
// at path k, {kind:2,k,v,i} cuts the array at index i and appends v, {kind:3,k} deletes path k.
// We replay each file incrementally. The old format (.json) is one full JSON object.

const maxVSCodeFile = 64 << 20

// An open VS Code request with no file change for this time is not active (window closed or crashed).
const vscodeActiveMS = 30 * 60 * 1000

// vscodeAppData gives the application-data folder of VS Code. VSCODE_APPDATA overrides the
// platform default. Relative values are ignored.
func vscodeAppData(getenv func(string) string, goos, home string) string {
	abs := func(v string) string {
		if v != "" && filepath.IsAbs(v) {
			return v
		}
		return ""
	}
	if v := abs(getenv("VSCODE_APPDATA")); v != "" {
		return v
	}
	switch goos {
	case "windows":
		if v := abs(getenv("APPDATA")); v != "" {
			return v
		}
		return filepath.Join(home, "AppData", "Roaming")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support")
	}
	if v := abs(getenv("XDG_CONFIG_HOME")); v != "" {
		return v
	}
	return filepath.Join(home, ".config")
}

func vscodeUserDirs(home string) []string {
	base := vscodeAppData(os.Getenv, runtime.GOOS, home)
	var out []string
	for _, n := range []string{"Code", "Code - Insiders", "Cursor", "VSCodium"} {
		out = append(out, filepath.Join(base, n, "User"))
	}
	return out
}

type vscodeToolRec struct {
	startedAt, endAt int64
}

type vscodeEntry struct {
	scanStart   int64 // time of the first VS Code scan; 0 = unknown
	ino         uint64
	offset      int64
	partial     []byte
	replay      bool
	state       any
	tools       map[string]*vscodeToolRec
	initialDone bool
	lastTrackAt int64
	head, tail  []byte
	usage       map[string]usageTotals
	llm         []Flow
	folder      string
	folders     []string
	workspace   string // the saved .code-workspace file of the window, or ""
	mtime       int64
}

// ---- Helpers for decoded JSON ----

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// get follows map keys. It returns nil when a step is missing.
func get(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func i64(v any) int64 {
	f, _ := num(v)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int64(f)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	default:
		return true
	}
}

func isNum(v any, want float64) bool {
	f, ok := num(v)
	return ok && f == want
}

// ---- Mutation log replay ----

type vscodeMutation struct {
	Kind *int `json:"kind"`
	K    any  `json:"k"`
	V    any  `json:"v"`
	I    any  `json:"i"`
}

func pathKey(k any) string {
	switch x := k.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(k)
}

func sliceIndex(k any) (int, bool) {
	f, ok := num(k)
	if !ok || f < 0 || f != math.Trunc(f) || f > math.MaxInt32 {
		return 0, false
	}
	return int(f), true
}

func childOf(node, k any) any {
	switch n := node.(type) {
	case map[string]any:
		return n[pathKey(k)]
	case []any:
		if i, ok := sliceIndex(k); ok && i < len(n) {
			return n[i]
		}
	}
	return nil
}

// putChild sets node[k] = v and returns the node, because a slice can grow.
func putChild(node, k, v any) any {
	switch n := node.(type) {
	case map[string]any:
		n[pathKey(k)] = v
	case []any:
		i, ok := sliceIndex(k)
		if !ok || i > len(n)+1<<16 {
			return n
		}
		for len(n) <= i {
			n = append(n, nil)
		}
		n[i] = v
		return n
	}
	return node
}

func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

func mutatePath(node any, k []any, kind int, v, idx any) any {
	key := k[0]
	if len(k) > 1 {
		child := childOf(node, key)
		if !isContainer(child) {
			if _, next := k[1].(float64); next {
				child = []any{}
			} else {
				child = map[string]any{}
			}
		}
		return putChild(node, key, mutatePath(child, k[1:], kind, v, idx))
	}
	switch kind {
	case 1:
		return putChild(node, key, v)
	case 2:
		arr := asSlice(childOf(node, key))
		if f, ok := num(idx); ok {
			if f < 0 {
				return node
			}
			arr = arr[:min(len(arr), int(f))]
		}
		if items, ok := v.([]any); ok {
			arr = append(arr, items...)
		}
		if arr == nil {
			arr = []any{}
		}
		return putChild(node, key, arr)
	case 3:
		switch n := node.(type) {
		case map[string]any:
			delete(n, pathKey(key))
		case []any:
			if i, ok := sliceIndex(key); ok && i < len(n) {
				n[i] = nil
			}
		}
	}
	return node
}

// badKeys are path keys that the JS collector rejects, because they reach the prototype of an object.
var badKeys = set("__proto__", "constructor", "prototype")

// safeKey tells if a path key is a string or a number, and is not one of badKeys.
func safeKey(x any) bool {
	switch k := x.(type) {
	case float64:
		return true
	case string:
		return !badKeys[k]
	}
	return false
}

// mutate applies one log entry to the state and returns the new state.
func mutate(root any, e *vscodeMutation) any {
	if e.Kind == nil {
		return root
	}
	if *e.Kind == 0 {
		return e.V
	}
	k := asSlice(e.K)
	for _, x := range k {
		if !safeKey(x) {
			return root
		}
	}
	if len(k) == 0 {
		if *e.Kind == 1 {
			return e.V
		}
		return root
	}
	if !isContainer(root) {
		return root
	}
	return mutatePath(root, k, *e.Kind, e.V, e.I)
}

// checkBytes is the size of the start and of the last read bytes that are kept to find a rewrite.
const checkBytes = 4096

// reset resets only the reader. The state, the tool times, and the seen token totals of the session
// stay until the reader parses a full-state line of the new log (replay). Thus a poll during a rewrite
// does not show old work as new, and a rewrite does not change the times of known tools or count the tokens again.
func (e *vscodeEntry) reset(ino uint64) {
	e.ino, e.offset, e.partial, e.replay = ino, 0, nil, true
	if e.state == nil {
		e.state = map[string]any{}
	}
	if e.tools == nil {
		e.tools = map[string]*vscodeToolRec{}
	}
	e.head, e.tail = nil, nil
}

// sameBytes tells if the file has the bytes want at pos.
func sameBytes(f *os.File, pos int64, want []byte) bool {
	if len(want) == 0 {
		return true
	}
	buf := make([]byte, len(want))
	n, _ := f.ReadAt(buf, pos)
	return n == len(want) && bytes.Equal(buf, want)
}

func (e *vscodeEntry) readLog(file string, size, mtime int64, ino uint64) error {
	if e.state == nil || e.ino != ino || size < e.offset {
		e.reset(ino)
	}
	// The old .json format is one object that VS Code writes again in place. Use the time, not the size.
	if strings.HasSuffix(file, ".json") {
		if e.mtime == mtime && e.offset == size {
			return nil
		}
		// A partly written file keeps the old state. The next poll reads it again.
		b, err := os.ReadFile(file)
		if err != nil {
			return nil
		}
		var s any
		if err := json.Unmarshal(b, &s); err != nil {
			return nil
		}
		e.state, e.offset, e.mtime, e.replay = s, size, mtime, false
		if e.state == nil {
			e.state = map[string]any{}
		}
		return nil
	}
	if e.mtime == mtime && size == e.offset {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	// VS Code can replace the log with one full-state line on the same inode. Then the size does
	// not grow while the time changes, or the bytes at the start or before the read position are different.
	if e.offset > 0 && (size == e.offset || !(sameBytes(f, 0, e.head) && sameBytes(f, e.offset-int64(len(e.tail)), e.tail))) {
		e.reset(ino)
	}
	e.mtime = mtime
	if size == e.offset {
		return nil
	}
	buf := make([]byte, size-e.offset)
	n, err := f.ReadAt(buf, e.offset)
	if err != nil && err != io.EOF {
		return err
	}
	buf = buf[:n]
	if len(e.head) < checkBytes {
		e.head = append(e.head, buf[:min(len(buf), checkBytes-len(e.head))]...)
	}
	if len(buf) >= checkBytes {
		e.tail = append([]byte(nil), buf[len(buf)-checkBytes:]...)
	} else {
		t := append(append([]byte(nil), e.tail...), buf...)
		e.tail = t[max(0, len(t)-checkBytes):]
	}
	e.offset += int64(n)
	text := append(e.partial, buf...)
	nl := bytes.LastIndexByte(text, '\n')
	e.partial = append([]byte(nil), text[nl+1:]...)
	for rest := text[:nl+1]; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		line := rest[:i]
		rest = rest[i+1:]
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m vscodeMutation
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if !e.replay {
			e.state = mutate(e.state, &m)
		} else if m.Kind != nil && *m.Kind == 0 {
			e.state, e.replay = m.V, false
		}
	}
	if e.state == nil {
		e.state = map[string]any{}
	}
	return nil
}

// ---- Tool classification ----

// Built-in VS Code chat tools and the destinations of their data. "" means no destination.
var vscodeToolSinks = map[string]string{
	"run_in_terminal": "Shell", "Run in Terminal": "Shell", "send_to_terminal": "Shell", "get_terminal_output": "Shell",
	"kill_terminal": "Shell", "await_terminal": "Shell", "terminal_last_command": "Shell",
	"runTests": "Build / run", "install_python_packages": "Build / run", "configure_python_environment": "Build / run",
	"copilot_readFile": "Filesystem", "copilot_findTextInFiles": "Filesystem", "copilot_createFile": "Filesystem", "copilot_replaceString": "Filesystem",
	"copilot_multiReplaceString": "Filesystem", "copilot_applyPatch": "Filesystem", "copilot_insertEdit": "Filesystem", "copilot_listDirectory": "Filesystem",
	"copilot_findFiles": "Filesystem", "copilot_getErrors": "Filesystem", "copilot_viewImage": "Filesystem", "copilot_searchCodebase": "Codebase search",
	"copilot_createDirectory": "Filesystem", "copilot_getChangedFiles": "Filesystem", "copilot_readProjectStructure": "Filesystem",
	"copilot_searchWorkspaceSymbols": "Filesystem", "copilot_findTestFiles": "Filesystem", "copilot_editNotebook": "Filesystem",
	"copilot_memory":       "Memory (local)",
	"copilot_fetchWebPage": "Web", "vscode_fetchWebPage_internal": "Web", "open_browser_page": "Web", "navigate_page": "Web",
	"screenshot_page": "Web", "read_page": "Web", "click_element": "Web", "run_playwright_code": "Web", "copilot_openSimpleBrowser": "Web",
	"copilot_githubTextSearch": "GitHub", "copilot_githubRepo": "GitHub",
	"copilot_sessionStoreSql": "Session DB",
	"vscode_askQuestions":     "You", "copilot_askQuestions": "You",
	"manage_todo_list": "", "tool_search": "", "task_complete": "", "runSubagent": "",
}

var (
	reMDLink     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reVSCodeMCP  = regexp.MustCompile(`^mcp_([^_]+)_`)
	reSubagentID = regexp.MustCompile(`(?i)subagent`)
	reJSONLExt   = regexp.MustCompile(`\.jsonl?$`)
)

func msgText(m any) string {
	if mm, ok := m.(map[string]any); ok {
		m = mm["value"]
	}
	if !truthy(m) {
		return ""
	}
	return reMDLink.ReplaceAllString(str(m), "$1")
}

func terminalCommand(t map[string]any) string {
	sd := asMap(t["toolSpecificData"])
	if str(sd["kind"]) != "terminal" || !truthy(sd["commandLine"]) {
		return ""
	}
	c := asMap(sd["commandLine"])
	for _, k := range []string{"userEdited", "toolEdited", "original"} {
		if v := c[k]; v != nil {
			return str(v)
		}
	}
	return str(c["forDisplay"])
}

func classifyVSCodeTool(t map[string]any) string {
	id := ""
	if truthy(t["toolId"]) {
		id = str(t["toolId"])
	}
	if src := asMap(t["source"]); str(src["type"]) == "mcp" && truthy(src["label"]) {
		return "MCP: " + str(src["label"])
	}
	if m := reVSCodeMCP.FindStringSubmatch(id); m != nil {
		return "MCP: " + m[1]
	}
	if reSubagentID.MatchString(id) || str(get(t, "toolSpecificData", "kind")) == "subagent" {
		return ""
	}
	sink := vscodeToolSinks[id]
	if sink == "Shell" {
		return classifyTool("bash", map[string]any{"command": terminalCommand(t)}, "", nil)
	}
	return sink
}

func describeVSCodeTool(t map[string]any) string {
	if cmd := terminalCommand(t); cmd != "" {
		return "$ " + cmd
	}
	if sd := asMap(t["toolSpecificData"]); str(sd["kind"]) == "subagent" {
		if truthy(sd["description"]) {
			return trunc(str(sd["description"]), 400)
		}
		if truthy(sd["prompt"]) {
			return trunc(str(sd["prompt"]), 400)
		}
		return ""
	}
	if s := msgText(t["invocationMessage"]); s != "" {
		return trunc(s, 400)
	}
	return trunc(msgText(t["pastTenseMessage"]), 400)
}

// ---- Derivation ----

func isToolPart(x map[string]any) bool {
	k := str(x["kind"])
	return (k == "toolInvocationSerialized" || k == "toolInvocation") && truthy(x["toolCallId"])
}

func reqEnd(r map[string]any) int64 {
	if v := i64(get(r, "modelState", "completedAt")); v != 0 {
		return v
	}
	ts := i64(r["timestamp"])
	if el := i64(r["elapsedMs"]); ts != 0 && el != 0 {
		return ts + el
	}
	if tot := i64(get(r, "result", "timings", "totalElapsed")); ts != 0 && tot != 0 {
		return ts + tot
	}
	return 0
}

func reqPending(r map[string]any) bool {
	if r == nil {
		return false
	}
	v := get(r, "modelState", "value")
	return isNum(v, 0) || isNum(v, 4) || (r["modelState"] == nil && !truthy(r["result"]) && reqEnd(r) == 0 && !truthy(r["isCanceled"]))
}

func modelName(id any) string {
	if !truthy(id) {
		return ""
	}
	return strings.TrimPrefix(str(id), "copilot/")
}

// firstPresent works like the JS expression "num(a ?? b)": the first value that is not null
// gives the count. A value that is not a finite non-negative number counts as 0.
func firstPresent(vs ...any) int64 {
	for _, v := range vs {
		if v != nil {
			return count(v)
		}
	}
	return 0
}

type modelUse struct {
	model     string
	tin, tout int64
}

func tokenCount(v any) bool {
	f, ok := num(v)
	return ok && !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0
}

// requestUsage gives the token use of one request for each model. VS Code stores whole-request
// totals for each model in modelTotals. Older records have only the request counters, for the
// model of the request.
func requestUsage(r map[string]any, mname string) []modelUse {
	var out []modelUse
	for _, x := range asSlice(r["modelTotals"]) {
		t := asMap(x)
		if t == nil || !truthy(t["model"]) || !tokenCount(t["inputTokens"]) || !tokenCount(t["outputTokens"]) {
			continue
		}
		out = append(out, modelUse{modelName(t["model"]), i64(t["inputTokens"]), i64(t["outputTokens"])})
	}
	if len(out) > 0 {
		return out
	}
	return []modelUse{{mname, firstPresent(r["promptTokens"], get(r, "result", "metadata", "promptTokens")),
		firstPresent(r["completionTokens"], get(r, "result", "metadata", "outputTokens"))}}
}

// vscodeToolDone tells if a tool part is done. VS Code writes unfinished tools with
// isComplete: true and summary.kind "incomplete".
func vscodeToolDone(t map[string]any) bool {
	return truthy(t["isComplete"]) && str(get(t, "summary", "kind")) != "incomplete"
}

// toolRejected tells if the user did not allow a tool. ToolConfirmKind 0 is Denied and 5 is
// Skipped. Old files store a denial as isConfirmed: false.
func toolRejected(t map[string]any) bool {
	c := t["isConfirmed"]
	if b, ok := c.(bool); ok && !b {
		return true
	}
	ty := get(c, "type")
	return isNum(ty, 0) || isNum(ty, 5)
}

type usageTotals struct{ tin, tout int64 }

// trackUsage records token use. VS Code stores only the token totals of each request, and they
// grow while the request runs. Each increase is recorded as a token observation at the poll that
// sees it, so that the 2-minute rates show recent use only. VS Code does not record model calls,
// so the call count is null (unknown). At the first read of a file, the totals of requests that started
// before the first VS Code scan are old use. Requests that started later (for example in a new chat) are recent use.
// The seen totals stay after VS Code writes the log again, so that they are not counted twice.
func (e *vscodeEntry) trackUsage(now int64) {
	first := e.usage == nil
	exact := !first && now-e.lastTrackAt <= trackGapMS
	since := e.lastTrackAt
	seen := map[string]bool{}
	if e.usage == nil {
		e.usage = map[string]usageTotals{}
	}
	for i, x := range asSlice(get(e.state, "requests")) {
		r := asMap(x)
		if r == nil {
			continue
		}
		rid := "#" + strconv.Itoa(i)
		if truthy(r["requestId"]) {
			rid = str(r["requestId"])
		}
		for _, u := range requestUsage(r, modelName(r["modelId"])) {
			if u.model == "" {
				continue
			}
			key := rid + "\n" + u.model
			prev := e.usage[key]
			seen[key] = true
			deltaIn, deltaOut := max(0, u.tin-prev.tin), max(0, u.tout-prev.tout)
			e.usage[key] = usageTotals{max(u.tin, prev.tin), max(u.tout, prev.tout)}
			if (deltaIn == 0 && deltaOut == 0) || (first && !(e.scanStart > 0 && i64(r["timestamp"]) > e.scanStart)) {
				continue
			}
			end, endAt := reqEnd(r), now
			if !exact && end > 0 && end >= since && end <= now {
				endAt = end
			}
			e.llm = append(e.llm, Flow{Sink: "LLM: " + u.model, Tool: "model", Model: u.model, Tin: deltaIn, Tout: deltaOut,
				Desc: fmt.Sprintf("%d in / %d out tokens", deltaIn, deltaOut), At: max(i64(r["timestamp"]), min(since, endAt)), EndAt: endAt,
				OK: !isNum(get(r, "modelState", "value"), 3), Tokens: deltaIn + deltaOut, Observation: true, CallsUnknown: true})
		}
	}
	for k := range e.usage {
		if !seen[k] {
			delete(e.usage, k)
		}
	}
	e.llm = recentFlows(e.llm, now)
	if len(e.llm) > maxLLM {
		e.llm = e.llm[len(e.llm)-maxLLM:]
	}
}

func recentFlows(fs []Flow, now int64) []Flow {
	out := []Flow{}
	for _, f := range fs {
		if now-f.EndAt < flowWindowMS {
			out = append(out, f)
		}
	}
	return out
}

// trackGapMS is the longest time between two collections that still gives exact tool times.
const trackGapMS = 15 * 1000

// trackTools records tool times. Tool parts have no timestamps. On the first read, we use
// the times of the request. After that, we record the poll time when a tool part appears
// and when it completes. After a gap in the collection (no poll), we use the request times
// again, but not before the last collection.
func (e *vscodeEntry) trackTools(now int64) {
	exact := e.initialDone && now-e.lastTrackAt <= trackGapMS
	since := int64(0)
	if e.initialDone {
		since = e.lastTrackAt
	}
	seen := map[string]bool{}
	for _, x := range asSlice(get(e.state, "requests")) {
		r := asMap(x)
		if r == nil {
			continue
		}
		for _, y := range asSlice(r["response"]) {
			t := asMap(y)
			if t == nil || !isToolPart(t) {
				continue
			}
			id := str(t["toolCallId"])
			seen[id] = true
			rec := e.tools[id]
			if rec == nil {
				rec = &vscodeToolRec{startedAt: now}
				if !exact {
					if ts := i64(r["timestamp"]); ts != 0 {
						rec.startedAt = max(ts, since)
					}
				}
				e.tools[id] = rec
			}
			if vscodeToolDone(t) && rec.endAt == 0 {
				if exact {
					rec.endAt = now
				} else if end := reqEnd(r); end != 0 {
					rec.endAt = end
				} else {
					rec.endAt = max(rec.startedAt, since)
				}
			}
		}
	}
	for k := range e.tools {
		if !seen[k] {
			delete(e.tools, k)
		}
	}
	e.initialDone = true
	e.lastTrackAt = now
}

func deriveVSCodeChat(file string, e *vscodeEntry, editor string, mtime, now int64) *Session {
	s := asMap(e.state)
	var reqs []map[string]any
	for _, x := range asSlice(s["requests"]) {
		if r := asMap(x); r != nil {
			reqs = append(reqs, r)
		}
	}
	var last map[string]any
	if len(reqs) > 0 {
		last = reqs[len(reqs)-1]
	}
	flows, runningTools := []Flow{}, []Tool{}
	subs := map[string]*Subagent{}
	var subOrder []string
	tokens, modelTokens := TokenCount{}, map[string]*ModelUsage{}
	model := modelName(get(s, "inputState", "selectedModel", "identifier"))
	if model == "" {
		model = modelName(get(s, "inputState", "selectedModel", "id"))
	}
	lastAssistant, unconfirmed := "", false
	for ri, r := range reqs {
		mname := modelName(r["modelId"])
		if mname != "" {
			model = mname
		}
		for _, u := range requestUsage(r, mname) {
			m, tin, tout := u.model, u.tin, u.tout
			if m == "" || (tin == 0 && tout == 0) {
				continue
			}
			tokens.In += tin
			tokens.Out += tout
			mt := modelTokens[m]
			if mt == nil {
				mt = &ModelUsage{CallsUnknown: true}
				modelTokens[m] = mt
			}
			mt.In += tin
			mt.Out += tout
		}
		pending := ri == len(reqs)-1 && reqPending(r)
		for _, y := range asSlice(r["response"]) {
			t := asMap(y)
			if t == nil {
				continue
			}
			if v, ok := t["value"].(string); ok && !truthy(t["kind"]) && strings.TrimSpace(v) != "" {
				lastAssistant = v
			}
			if !isToolPart(t) {
				continue
			}
			id := str(t["toolCallId"])
			rec := e.tools[id]
			if rec == nil {
				rec = &vscodeToolRec{startedAt: i64(r["timestamp"])}
				if rec.startedAt == 0 {
					rec.startedAt = now
				}
			}
			agentID := ""
			if truthy(t["subAgentInvocationId"]) {
				agentID = str(t["subAgentInvocationId"])
			}
			done := vscodeToolDone(t)
			if sd := asMap(t["toolSpecificData"]); str(sd["kind"]) == "subagent" {
				typ := "subagent"
				if truthy(sd["agentName"]) {
					typ = str(sd["agentName"])
				}
				name := typ
				if truthy(sd["agentDisplayName"]) {
					name = str(sd["agentDisplayName"])
				}
				smodel := sd["modelId"]
				if !truthy(smodel) {
					smodel = sd["modelName"]
				}
				lastAt := rec.endAt
				if lastAt == 0 {
					lastAt = now
				}
				said := ""
				if truthy(sd["description"]) {
					said = trunc(str(sd["description"]), 160)
				}
				if _, ok := subs[id]; !ok {
					subOrder = append(subOrder, id)
				}
				subs[id] = &Subagent{ID: id, Name: name, Type: typ, Model: modelName(smodel), StartedAt: rec.startedAt, LastAt: lastAt,
					Running: pending && !done, Said: said, EndedAt: rec.endAt}
				continue
			}
			sink, desc := classifyVSCodeTool(t), describeVSCodeTool(t)
			name := "tool"
			if truthy(t["toolId"]) {
				name = str(t["toolId"])
			}
			if !done && pending {
				runningTools = append(runningTools, Tool{ID: id, Name: name, Desc: desc, StartedAt: rec.startedAt, AgentID: agentID, Sink: sink})
				if t["isConfirmed"] == nil {
					unconfirmed = true
				}
			} else if done && sink != "" && rec.endAt != 0 && now-rec.endAt < flowWindowMS {
				flows = append(flows, Flow{Sink: sink, Tool: name, Desc: desc, At: rec.startedAt, EndAt: rec.endAt,
					OK: !toolRejected(t) && !truthy(t["resultError"]), AgentID: agentID})
			}
		}
	}
	subagents := []Subagent{}
	for _, id := range subOrder {
		a := subs[id]
		end := a.EndedAt
		if end == 0 {
			end = a.LastAt
		}
		if a.Running || now-end < 5*60e3 {
			subagents = append(subagents, *a)
		}
	}
	for _, t := range runningTools {
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
	fresh := now-mtime < vscodeActiveMS
	pendingReq := reqPending(last) || len(asSlice(s["pendingRequests"])) > 0
	waiting := fresh && (isNum(get(last, "modelState", "value"), 4) || unconfirmed)
	busy := fresh && !waiting && pendingReq
	intent := ""
	if n := len(runningTools); n > 0 {
		rt := runningTools[n-1]
		if rt.Desc != "" {
			intent = trunc(rt.Desc, 120)
		} else {
			intent = trunc(rt.Name, 120)
		}
	}
	lastText := ""
	if v := get(last, "message", "text"); truthy(v) {
		lastText = str(v)
	}
	name := "VS Code chat"
	if truthy(s["customTitle"]) {
		name = str(s["customTitle"])
	} else if lastText != "" {
		name = lastText
	}
	repo := ""
	if e.folder != "" {
		repo = filepath.Base(e.folder)
	}
	if !busy && !waiting {
		runningTools = []Tool{}
	}
	if len(flows) > maxFlows {
		flows = flows[len(flows)-maxFlows:]
	}
	flows = append(flows, recentFlows(e.llm, now)...)
	return &Session{
		Kind: "vscode-chat", ID: reJSONLExt.ReplaceAllString(filepath.Base(file), ""), Surface: editor + " chat", Live: true,
		Name: trunc(name, 90), Cwd: e.folder, Folders: e.folders, Workspace: e.workspace, Repo: repo, Model: model, Busy: busy, Waiting: waiting, LastEventAt: mtime,
		LastUser: trunc(lastText, 200), LastAssistant: trunc(lastAssistant, 240), Intent: intent,
		RunningTools: runningTools, Subagents: subagents, Flows: flows, Peers: []Peer{},
		Tokens: tokens, ModelTokens: modelTokens,
	}
}

// fileURIPath converts a file: URI to a local path.
func fileURIPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := u.Path
	// file:///C:/work becomes C:\work on Windows.
	if runtime.GOOS == "windows" && len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = filepath.FromSlash(p[1:])
	}
	return p
}

// jsonString gives the end (exclusive) of the JSON string that starts at b[i].
func jsonString(b []byte, i int) int {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(b)
}

// stripJSONC removes comments and then trailing commas (.code-workspace files). Strings stay as they are.
func stripJSONC(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte("\uFEFF"))
	noComments := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		switch {
		case b[i] == '"':
			j := jsonString(b, i)
			noComments = append(noComments, b[i:j]...)
			i = j
		case bytes.HasPrefix(b[i:], []byte("//")):
			if k := bytes.IndexByte(b[i:], '\n'); k >= 0 {
				i += k
			} else {
				i = len(b)
			}
		case bytes.HasPrefix(b[i:], []byte("/*")):
			if k := bytes.Index(b[i+2:], []byte("*/")); k >= 0 {
				i += k + 4
			} else {
				i = len(b)
			}
		default:
			noComments = append(noComments, b[i])
			i++
		}
	}
	b = noComments
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		switch b[i] {
		case '"':
			j := jsonString(b, i)
			out = append(out, b[i:j]...)
			i = j
			continue
		case ',':
			rest := bytes.TrimLeft(b[i+1:], " \t\r\n")
			if len(rest) > 0 && (rest[0] == '}' || rest[0] == ']') {
				i++
				continue
			}
		}
		out = append(out, b[i])
		i++
	}
	return out
}

// workspaceRoots gives the root folders of a VS Code window. A multi-root window refers to a
// .code-workspace file. saved is that file if the user saved it. VS Code can open a saved file to
// show the window again, but not the workspace.json of an unsaved ("untitled") workspace.
func workspaceRoots(wsDir string) (roots []string, saved string) {
	b, err := os.ReadFile(filepath.Join(wsDir, "workspace.json"))
	if err != nil {
		return nil, ""
	}
	var w map[string]any
	if json.Unmarshal(b, &w) != nil {
		return nil, ""
	}
	if truthy(w["folder"]) {
		raw := str(w["folder"])
		if strings.HasPrefix(raw, "file:") {
			if p := fileURIPath(raw); p != "" {
				return []string{p}, ""
			}
			return nil, ""
		}
		if u, err := url.PathUnescape(raw); err == nil && u != "" {
			return []string{u}, ""
		}
		return nil, ""
	}
	raw := str(w["workspace"])
	if !strings.HasPrefix(raw, "file:") {
		return nil, ""
	}
	file := fileURIPath(raw)
	if file == "" {
		return nil, ""
	}
	cb, err := os.ReadFile(file)
	if err != nil {
		return nil, ""
	}
	var cfg struct {
		Folders []map[string]any `json:"folders"`
	}
	if json.Unmarshal(stripJSONC(cb), &cfg) != nil {
		return nil, ""
	}
	if strings.HasSuffix(file, ".code-workspace") {
		saved = file
	}
	for _, f := range cfg.Folders {
		if p, ok := f["path"].(string); ok {
			p = filepath.FromSlash(p)
			if !filepath.IsAbs(p) {
				p = filepath.Join(filepath.Dir(file), p)
			}
			roots = append(roots, filepath.Clean(p))
		} else if u, ok := f["uri"].(string); ok && strings.HasPrefix(u, "file:") {
			if p := fileURIPath(u); p != "" {
				roots = append(roots, p)
			}
		}
	}
	return roots, saved
}

// chatCandidate is one chat file for a session ID.
type chatCandidate struct {
	file, wsDir, editor string
	st                  os.FileInfo
}

// preferChatFile tells if a is a better file than b for the same session. VS Code copies chat
// files when a window moves to a new workspace and keeps the originals. A session can also have
// a legacy .json file and a .jsonl file. Use the newest file, then .jsonl before .json, then the
// first path.
func preferChatFile(a, b chatCandidate) bool {
	if ma, mb := a.st.ModTime(), b.st.ModTime(); !ma.Equal(mb) {
		return ma.After(mb)
	}
	if ja, jb := strings.HasSuffix(a.file, ".jsonl"), strings.HasSuffix(b.file, ".jsonl"); ja != jb {
		return ja
	}
	return a.file < b.file
}

func (c *Collector) collectVSCodeChats(now int64) []*Session {
	if c.vscodeScanStart == 0 {
		c.vscodeScanStart = now
	}
	byID := map[string]chatCandidate{}
	var ids []string
	for _, userDir := range c.vscodeDirs {
		wsRoot := filepath.Join(userDir, "workspaceStorage")
		wsIDs, _ := os.ReadDir(wsRoot)
		editor := filepath.Base(filepath.Dir(userDir))
		type pair struct{ dir, wsDir string }
		var dirs []pair
		for _, w := range wsIDs {
			dirs = append(dirs, pair{filepath.Join(wsRoot, w.Name(), "chatSessions"), filepath.Join(wsRoot, w.Name())})
		}
		dirs = append(dirs, pair{filepath.Join(userDir, "globalStorage", "emptyWindowChatSessions"), ""})
		for _, d := range dirs {
			files, err := os.ReadDir(d.dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if !reJSONLExt.MatchString(f.Name()) {
					continue
				}
				file := filepath.Join(d.dir, f.Name())
				st, err := os.Stat(file)
				if err != nil {
					continue
				}
				if now-st.ModTime().UnixMilli() > vscodeActiveMS || st.Size() > maxVSCodeFile {
					continue
				}
				cand := chatCandidate{file: file, wsDir: d.wsDir, editor: editor, st: st}
				id := reJSONLExt.ReplaceAllString(f.Name(), "")
				if old, ok := byID[id]; !ok {
					ids = append(ids, id)
					byID[id] = cand
				} else if preferChatFile(cand, old) {
					byID[id] = cand
				}
			}
		}
	}
	var out []*Session
	seen := map[string]bool{}
	for _, id := range ids {
		cand := byID[id]
		file, st := cand.file, cand.st
		mt := st.ModTime().UnixMilli()
		seen[file] = true
		e := c.vscode[file]
		if e == nil {
			e = &vscodeEntry{}
			if cand.wsDir != "" {
				e.folders, e.workspace = workspaceRoots(cand.wsDir)
				if len(e.folders) > 0 {
					e.folder = e.folders[0]
				}
			}
			c.vscode[file] = e
		}
		if err := e.readLog(file, st.Size(), mt, fileIno(st)); err != nil {
			delete(c.vscode, file)
			continue
		}
		// Until the full-state line of a new or rewritten log is parsed, keep the tracking data as it is.
		if !e.replay {
			e.scanStart = c.vscodeScanStart
			e.trackUsage(now)
			e.trackTools(now)
		}
		out = append(out, deriveVSCodeChat(file, e, cand.editor, mt, now))
	}
	for k := range c.vscode {
		if !seen[k] {
			delete(c.vscode, k)
		}
	}
	return out
}
