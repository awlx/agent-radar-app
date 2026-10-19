package demo

import "testing"

func TestSnapshot(t *testing.T) {
	snap := New().Snapshot()
	working, waiting := 0, 0
	for _, s := range snap.Sessions {
		if s.Waiting {
			waiting++
		} else if s.Busy {
			working++
		}
		for _, f := range s.Flows {
			if f.At > snap.Now || f.At <= snap.Now-120_000 {
				t.Fatalf("%s: flow at %d is not in the last 2 minutes", s.Name, snap.Now-f.At)
			}
		}
	}
	if working == 0 || waiting == 0 {
		t.Fatalf("working=%d waiting=%d", working, waiting)
	}
	for _, parent := range snap.Sessions {
		for _, id := range parent.ChildSessions {
			for _, child := range snap.Sessions {
				if child.ID == id && len(child.Subagents) > 0 {
					return
				}
			}
		}
	}
	t.Fatal("demo must include a parent -> child session -> subagent chain")
}
