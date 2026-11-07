package pill

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
)

// niriFocus follows niri's IPC event stream.
var niriFocus = focusSource{
	available: func() bool { return os.Getenv("NIRI_SOCKET") != "" },
	watch:     watchNiri,
}

func watchNiri(stop <-chan struct{}, changed func(output string)) {
	conn, err := net.Dial("unix", os.Getenv("NIRI_SOCKET"))
	if err != nil {
		return
	}
	go func() {
		<-stop
		conn.Close()
	}()
	if _, err := conn.Write([]byte("\"EventStream\"\n")); err != nil {
		return
	}
	var f focusTracker
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		if name, ok := f.line(line); ok {
			changed(name)
		}
	}
}

// focusTracker follows the focused workspace's screen through niri's events.
type focusTracker struct {
	outputs map[uint64]string // workspace id to screen
	focused string
}

// line takes one event and returns the screen in use when it changed.
func (f *focusTracker) line(b []byte) (string, bool) {
	var ev struct {
		WorkspacesChanged *struct {
			Workspaces []struct {
				ID        uint64 `json:"id"`
				Output    string `json:"output"`
				IsFocused bool   `json:"is_focused"`
			} `json:"workspaces"`
		}
		WorkspaceActivated *struct {
			ID      uint64 `json:"id"`
			Focused bool   `json:"focused"`
		}
	}
	if json.Unmarshal(b, &ev) != nil {
		return "", false
	}
	name := f.focused
	switch {
	case ev.WorkspacesChanged != nil:
		f.outputs = map[uint64]string{}
		for _, w := range ev.WorkspacesChanged.Workspaces {
			f.outputs[w.ID] = w.Output
			if w.IsFocused {
				name = w.Output
			}
		}
	case ev.WorkspaceActivated != nil && ev.WorkspaceActivated.Focused:
		name = f.outputs[ev.WorkspaceActivated.ID]
	}
	if name == f.focused {
		return "", false
	}
	f.focused = name
	return name, true
}
