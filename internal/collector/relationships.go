package collector

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

type sessionRef struct {
	id      string
	aliases []string
}

var sessionResultID = regexp.MustCompile(`(?im)^(?:id|session id|copilot session id):\s*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\s*$`)
var createdSessionResultID = regexp.MustCompile(`(?im)^Created session '[^\r\n]*' \(id:\s*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\)`)

// Read identity fields, never the first UUID in arbitrary tool output (which may be a project ID).
func createdSession(raw json.RawMessage) (sessionRef, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return sessionRef{}, false
	}
	var read func(any, int) (sessionRef, bool)
	read = func(value any, depth int) (sessionRef, bool) {
		if depth > 6 {
			return sessionRef{}, false
		}
		switch v := value.(type) {
		case map[string]any:
			if failed, _ := v["isError"].(bool); failed {
				return sessionRef{}, false
			}
			var ids []string
			for _, key := range []string{"copilot_session_id", "copilotSessionId", "session_id", "sessionId", "id", "project_session_id", "projectSessionId"} {
				if id, ok := v[key].(string); ok && reUUID.FindString(id) == id && id != "" {
					ids = append(ids, strings.ToLower(id))
				}
			}
			if len(ids) > 0 {
				created, _ := v["created"].(bool)
				return sessionRef{id: ids[0], aliases: ids[1:]}, created
			}
			for _, key := range []string{"content", "text"} {
				if ref, created := read(v[key], depth+1); ref.id != "" {
					return ref, created
				}
			}
		case []any:
			for _, item := range v {
				if ref, created := read(item, depth+1); ref.id != "" {
					return ref, created
				}
			}
		case string:
			var inner any
			if json.Unmarshal([]byte(v), &inner) == nil {
				return read(inner, depth+1)
			}
			if m := createdSessionResultID.FindStringSubmatch(v); m != nil {
				return sessionRef{id: strings.ToLower(m[1])}, true
			}
			if m := sessionResultID.FindStringSubmatch(v); m != nil {
				return sessionRef{id: strings.ToLower(m[1])}, strings.HasPrefix(strings.TrimSpace(v), "Created session")
			}
		}
		return sessionRef{}, false
	}
	return read(value, 0)
}

// Resolve explicitly paired app/CLI IDs onto existing collector sessions. Never create alias nodes.
func resolveSessionRelations(sessions []*Session, entries map[string]*sessionEntry) {
	exists := map[string]bool{}
	for _, s := range sessions {
		exists[s.ID] = true
	}
	aliases := map[string]string{}
	for _, s := range sessions {
		e := entries[s.ID]
		if e == nil || e.p == nil {
			continue
		}
		for _, ref := range e.p.children {
			ids := append([]string{ref.id}, ref.aliases...)
			canonical := ""
			for _, id := range ids {
				if exists[id] {
					if canonical != "" && canonical != id {
						canonical = ""
						break
					}
					canonical = id
				}
			}
			if canonical != "" {
				for _, id := range ids {
					if previous, ok := aliases[id]; ok && previous != canonical {
						aliases[id] = ""
					} else if !ok {
						aliases[id] = canonical
					}
				}
			}
		}
	}
	resolve := func(id string) string {
		if exists[id] {
			return id
		}
		return aliases[id]
	}
	for _, s := range sessions {
		e := entries[s.ID]
		if e == nil || e.p == nil {
			continue
		}
		children := map[string]bool{}
		s.ChildSessions = nil
		for _, ref := range e.p.children {
			if id := resolve(ref.id); id != "" && id != s.ID {
				children[id] = true
			}
		}
		for id := range children {
			s.ChildSessions = append(s.ChildSessions, id)
		}
		sort.Strings(s.ChildSessions)
		for i := range s.Peers {
			if id := resolve(s.Peers[i].To); id != "" {
				s.Peers[i].To = id
			}
		}
	}
}
