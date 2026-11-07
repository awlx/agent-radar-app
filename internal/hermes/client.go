package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The kinds of Hermes servers.
const (
	kindDashboard = "dashboard" // hermes dashboard (default port 9119)
	kindAPI       = "api"       // the OpenAI-compatible API server (default port 8642)
)

const maxBody = 8 << 20

var errUnauthorized = errors.New("hermes: not authorized (check the token or the username and password)")

type client struct {
	cfg  *Config
	http *http.Client

	mu       sync.Mutex
	kind     string
	loggedIn bool
}

func newClient(cfg *Config) *client {
	jar, _ := cookiejar.New(nil)
	return &client{cfg: cfg, http: &http.Client{
		Timeout: 8 * time.Second,
		Jar:     jar,
		// Do not follow redirects: a redirect to a login page is a failure, and the
		// credentials must not go to a different host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type status struct {
	GatewayState  *string        `json:"gateway_state"`
	AuthProviders []string       `json:"auth_providers"`
	ActiveAgents  int            `json:"active_agents"`
	Busy          bool           `json:"gateway_busy"`
	Version       string         `json:"version"`
	Platforms     map[string]any `json:"gateway_platforms"`
}

// detect finds the kind of server. The dashboard has a public /api/status. The API server has /v1/capabilities.
func (c *client) detect(ctx context.Context) (string, error) {
	c.mu.Lock()
	k := c.kind
	c.mu.Unlock()
	if k != "" {
		return k, nil
	}
	var st status
	code, err := c.do(ctx, http.MethodGet, "/api/status", nil, &st, false)
	if err != nil && code == 0 {
		return "", err // network or TLS error: the next request fails the same way
	}
	if err == nil && code == 200 && (st.GatewayState != nil || st.AuthProviders != nil) {
		k = kindDashboard
	} else {
		var caps map[string]any
		code, err := c.do(ctx, http.MethodGet, "/v1/capabilities", nil, &caps, true)
		switch {
		case err == nil && code == 200:
			k = kindAPI
		case code == 401 || code == 403:
			return "", errUnauthorized
		case err != nil:
			return "", err
		default:
			return "", fmt.Errorf("hermes: %s is not a Hermes dashboard or API server (HTTP %d)", c.cfg.base, code)
		}
	}
	c.mu.Lock()
	c.kind = k
	c.mu.Unlock()
	return k, nil
}

// login signs in to the dashboard with the username and the password. The dashboard sets a session cookie.
func (c *client) login(ctx context.Context) error {
	if c.cfg.Username == "" || c.cfg.Password == "" {
		return errUnauthorized
	}
	provider := "basic"
	var ps struct {
		Providers []struct {
			Name             string `json:"name"`
			SupportsPassword bool   `json:"supports_password"`
		} `json:"providers"`
	}
	if code, err := c.do(ctx, http.MethodGet, "/api/auth/providers", nil, &ps, false); err == nil && code == 200 {
		for _, p := range ps.Providers {
			if p.SupportsPassword {
				provider = p.Name
				break
			}
		}
	}
	body := map[string]string{"provider": provider, "username": c.cfg.Username, "password": c.cfg.Password, "next": "/"}
	code, err := c.do(ctx, http.MethodPost, "/auth/password-login", body, nil, false)
	if err != nil {
		return err
	}
	if code == 429 {
		return errors.New("hermes: too many login attempts; wait and try again")
	}
	if code != 200 {
		return errUnauthorized
	}
	c.mu.Lock()
	c.loggedIn = true
	c.mu.Unlock()
	return nil
}

// get reads JSON from path. On the dashboard with a password, it signs in when necessary, one time per call.
func (c *client) get(ctx context.Context, path string, out any) error {
	kind, err := c.detect(ctx)
	if err != nil {
		return err
	}
	usePassword := kind == kindDashboard && c.cfg.Token == ""
	c.mu.Lock()
	need := usePassword && !c.loggedIn
	c.mu.Unlock()
	if need {
		if err := c.login(ctx); err != nil {
			return err
		}
	}
	code, err := c.do(ctx, http.MethodGet, path, nil, out, true)
	if err == nil && code == 401 && usePassword {
		c.mu.Lock()
		c.loggedIn = false
		c.mu.Unlock()
		if err := c.login(ctx); err != nil {
			return err
		}
		code, err = c.do(ctx, http.MethodGet, path, nil, out, true)
	}
	switch {
	case err != nil:
		return err
	case code == 401 || code == 403:
		return errUnauthorized
	case code != 200:
		return &httpError{code: code, path: strings.SplitN(path, "?", 2)[0]}
	}
	return nil
}

type httpError struct {
	code int
	path string
}

func (e *httpError) Error() string { return fmt.Sprintf("hermes: GET %s: HTTP %d", e.path, e.code) }

// do sends one request. auth adds the bearer token. It decodes a 200 response into out.
func (c *client) do(ctx context.Context, method, path string, body, out any, auth bool) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth && c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The error text can contain the URL, but never the credentials.
		var ue *url.Error
		if errors.As(err, &ue) {
			return 0, fmt.Errorf("hermes: %s %s: %v", method, strings.SplitN(path, "?", 2)[0], ue.Err)
		}
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("hermes: %s: invalid JSON", strings.SplitN(path, "?", 2)[0])
	}
	return resp.StatusCode, nil
}

// ---- Response types ----

// flexTime is a time in a Hermes response: Unix seconds (float), Unix milliseconds, or an RFC 3339 text.
type flexTime int64 // Unix milliseconds

func (t *flexTime) UnmarshalJSON(b []byte) error {
	*t = 0
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return nil
		}
		if f, err := strconv.ParseFloat(str, 64); err == nil {
			*t = fromNumber(f)
			return nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02 15:04:05.999999"} {
			if v, err := time.Parse(layout, str); err == nil {
				*t = flexTime(v.UnixMilli())
				return nil
			}
		}
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		*t = fromNumber(f)
	}
	return nil
}

func fromNumber(f float64) flexTime {
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0
	}
	if f < 1e11 { // seconds
		return flexTime(f * 1000)
	}
	return flexTime(f)
}

// flexInt is a count that can be null or a float.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	*n = 0
	var f float64
	if json.Unmarshal(b, &f) == nil && f > 0 && f < math.MaxInt64/2 {
		*n = flexInt(f)
	}
	return nil
}

type sessionRow struct {
	ID            string   `json:"id"`
	Source        string   `json:"source"`
	Model         string   `json:"model"`
	Title         string   `json:"title"`
	Preview       string   `json:"preview"`
	Cwd           string   `json:"cwd"`
	StartedAt     flexTime `json:"started_at"`
	EndedAt       flexTime `json:"ended_at"`
	LastActive    flexTime `json:"last_active"`
	LastActivity  flexTime `json:"last_activity_at"`
	ActivityDesc  string   `json:"last_activity_description"`
	IsActive      *bool    `json:"is_active"`
	MessageCount  flexInt  `json:"message_count"`
	InputTokens   flexInt  `json:"input_tokens"`
	OutputTokens  flexInt  `json:"output_tokens"`
	APICallCount  flexInt  `json:"api_call_count"`
	ParentSession string   `json:"parent_session_id"`
}

func (r *sessionRow) last() int64 {
	return max(int64(r.LastActive), int64(r.LastActivity), int64(r.StartedAt))
}

type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
	ToolName   string          `json:"tool_name"`
	ToolCallID string          `json:"tool_call_id"`
	Timestamp  flexTime        `json:"timestamp"`
}

type toolCall struct {
	ID       string `json:"id"`
	CallID   string `json:"call_id"`
	Name     string `json:"name"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
	Arguments json.RawMessage `json:"arguments"`
}

// calls decodes the tool calls of a message. Hermes keeps them as a JSON array or as a JSON text of an array.
func (m *message) calls() []toolCall {
	raw := m.ToolCalls
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var out []toolCall
	_ = json.Unmarshal(raw, &out)
	return out
}

func (t *toolCall) name() string {
	if t.Function.Name != "" {
		return t.Function.Name
	}
	return t.Name
}

func (t *toolCall) id() string {
	if t.ID != "" {
		return t.ID
	}
	return t.CallID
}

func (t *toolCall) args() map[string]any {
	raw := t.Function.Arguments
	if len(raw) == 0 {
		raw = t.Arguments
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// text gives the text of a message content: a string, or a list of parts with "text".
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// ---- Endpoints ----

func (c *client) query(v url.Values) string {
	c.mu.Lock()
	dash := c.kind == kindDashboard
	c.mu.Unlock()
	if dash && c.cfg.Profile != "" {
		v.Set("profile", c.cfg.Profile)
	}
	return v.Encode()
}

// sessions lists the newest sessions. With subagents, it lists only the delegated child sessions.
func (c *client) sessions(ctx context.Context, limit int, subagents bool) ([]sessionRow, error) {
	if _, err := c.detect(ctx); err != nil {
		return nil, err
	}
	v := url.Values{"limit": {strconv.Itoa(limit)}}
	if subagents {
		v.Set("source", "subagent")
	}
	v.Set("order", "recent")
	var out struct {
		Sessions []sessionRow `json:"sessions"`
		Data     []sessionRow `json:"data"`
	}
	err := c.get(ctx, "/api/sessions?"+c.query(v), &out)
	var he *httpError
	if errors.As(err, &he) && (he.code == 400 || he.code == 422) {
		// An older server does not know order=recent.
		v.Del("order")
		err = c.get(ctx, "/api/sessions?"+c.query(v), &out)
	}
	if err != nil {
		return nil, err
	}
	rows := append(out.Sessions, out.Data...)
	if subagents {
		kept := rows[:0]
		for _, r := range rows {
			// parent_session_id alone is not enough: a continued session (after a reset or a compression) has one too.
			if r.Source == "subagent" && r.ParentSession != "" {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	return rows, nil
}

// messages gives the newest messages of a session, oldest first.
func (c *client) messages(ctx context.Context, id string, limit int) ([]message, error) {
	v := url.Values{"limit": {strconv.Itoa(limit)}, "order": {"latest"}}
	var out struct {
		Messages []message `json:"messages"`
		Data     []message `json:"data"`
	}
	if err := c.get(ctx, "/api/sessions/"+url.PathEscape(id)+"/messages?"+c.query(v), &out); err != nil {
		return nil, err
	}
	msgs := append(out.Messages, out.Data...)
	// The order of a "latest" page is not certain. Sort by time, and keep the order for equal times.
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Timestamp < msgs[j].Timestamp })
	return msgs, nil
}
