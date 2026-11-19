package config

import "testing"

func TestInjectionForApp(t *testing.T) {
	on, off := true, false
	c := Injection{AppendSpace: true, AppendEnter: false, Apps: map[string]AppInjection{
		"slack": {Enter: &on},
		"code":  {Space: &off},
	}}
	if g := c.ForApp("slack"); !g.AppendSpace || !g.AppendEnter {
		t.Errorf("slack: %+v", g)
	}
	if g := c.ForApp("code"); g.AppendSpace || g.AppendEnter {
		t.Errorf("code: %+v", g)
	}
	if g := c.ForApp("other"); !g.AppendSpace || g.AppendEnter {
		t.Errorf("other: %+v", g)
	}
	if g := c.ForApp(""); !g.AppendSpace || g.AppendEnter {
		t.Errorf("none: %+v", g)
	}
}

func TestHistorySkips(t *testing.T) {
	h := History{SkipApps: []string{"keepassxc"}}
	if !h.Skips("keepassxc") || h.Skips("slack") || h.Skips("") {
		t.Error("Skips")
	}
}
