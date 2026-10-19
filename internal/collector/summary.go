package collector

import "time"

// ---- Summary (for -snapshot and /api/snapshot) ----

type summary struct {
	At            string           `json:"at"`
	VSCodeWindows []summaryIDE     `json:"vscodeWindows"`
	Sessions      []summarySession `json:"sessions"`
}

type summaryIDE struct {
	IDE     string   `json:"ide"`
	Folders []string `json:"folders"`
}

type summarySession struct {
	ID                     string                 `json:"id"`
	Name                   string                 `json:"name"`
	Surface                string                 `json:"surface"`
	Repo                   string                 `json:"repo"`
	Model                  string                 `json:"model"`
	Status                 string                 `json:"status"`
	LastActivitySecondsAgo int64                  `json:"lastActivitySecondsAgo"`
	RunningTools           []string               `json:"runningTools"`
	Subagents              []summarySub           `json:"subagents"`
	BackgroundShells       []BackgroundShell      `json:"backgroundShells,omitempty"`
	DataFlowsLast2Min      []string               `json:"dataFlowsLast2Min"`
	ModelTokens            map[string]*ModelUsage `json:"modelTokens"`
}

type summarySub struct {
	Name    string `json:"name"`
	Model   string `json:"model"`
	Running bool   `json:"running"`
}

func Summarize(snap *Snapshot, activeOnly bool) summary {
	out := summary{At: time.UnixMilli(snap.Now).UTC().Format(time.RFC3339), VSCodeWindows: []summaryIDE{}, Sessions: []summarySession{}}
	var sessions []*Session
	for _, s := range snap.Sessions {
		if !activeOnly || s.Busy || s.Waiting || len(s.BackgroundShells) > 0 {
			sessions = append(sessions, s)
		}
	}
	for _, i := range snap.IDEs {
		inUse := false
		for _, s := range sessions {
			for _, c := range sessionRoots(s) {
				for _, f := range i.Folders {
					if inFolder(c, f) {
						inUse = true
					}
				}
			}
		}
		if !activeOnly || inUse {
			out.VSCodeWindows = append(out.VSCodeWindows, summaryIDE{IDE: i.IDE, Folders: i.Folders})
		}
	}
	for _, s := range sessions {
		st := "idle"
		if s.Waiting {
			st = "waiting"
		} else if s.Busy {
			st = "working"
		}
		ss := summarySession{ID: s.ID, Name: s.Name, Surface: s.Surface, Repo: s.Repo, Model: s.Model, Status: st,
			LastActivitySecondsAgo: (snap.Now - s.LastEventAt) / 1000, RunningTools: []string{}, Subagents: []summarySub{},
			DataFlowsLast2Min: []string{}, ModelTokens: s.ModelTokens}
		ss.BackgroundShells = s.BackgroundShells
		// Only tool names. Command arguments can contain credentials, so they stay in the local UI.
		for _, t := range s.RunningTools {
			ss.RunningTools = append(ss.RunningTools, t.Name)
		}
		for _, a := range s.Subagents {
			if !activeOnly || a.Running {
				ss.Subagents = append(ss.Subagents, summarySub{Name: a.Name, Model: a.Model, Running: a.Running})
			}
		}
		seen := map[string]bool{}
		for _, f := range s.Flows {
			if !seen[f.Sink] {
				seen[f.Sink] = true
				ss.DataFlowsLast2Min = append(ss.DataFlowsLast2Min, f.Sink)
			}
		}
		out.Sessions = append(out.Sessions, ss)
	}
	return out
}
