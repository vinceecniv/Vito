//go:build linux

package hotkey

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// niri has no runtime API for key bindings or keyboard options, so Caps Lock
// as the dictation key lives in the user's own config.kdl: caps:menu in the
// xkb options, and a bind on Menu whose press and release signal Vito
// (SIGRTMIN+1 / +2, read by Gesture). Vito writes both only when asked, keeps
// a copy of the file as it was, and lets niri check the result before it
// replaces anything. niri reloads its config by itself.

const niriMarker = "// Vito: Caps Lock"

// niriBind goes into the config's binds block (niri allows one per file),
// between Vito's marker and niriEnd, so it can be taken out again.
var niriBind = []string{
	niriMarker + " as the dictation key (hold to dictate, double-tap for hands-free)",
	`Menu repeat=false hotkey-overlay-title="Vito: dictate" {`,
	`    press   { spawn "pkill" "-RTMIN+1" "-f" "vito serve"; }`,
	`    release { spawn "pkill" "-RTMIN+2" "-f" "vito serve"; }`,
	`}`,
	niriEnd,
}

const niriEnd = "// Vito: end"

func niriSession() bool { return os.Getenv("NIRI_SOCKET") != "" }

// niriConfigPath is the file niri reads: $NIRI_CONFIG, else the user's.
func niriConfigPath() string {
	if p := os.Getenv("NIRI_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "niri", "config.kdl")
}

// NiriCapsStatus reads the config: is Caps Lock set up, and could Vito do it.
func NiriCapsStatus() NiriCaps {
	if !niriSession() {
		return NiriCaps{}
	}
	path := niriConfigPath()
	st := NiriCaps{State: "off", Path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		st.Manual = "no config file"
		return st
	}
	opt, bind := niriHas(string(b))
	switch {
	case opt && bind:
		st.State = "on"
	case opt || bind:
		st.State = "partial"
	}
	if _, err := exec.LookPath("niri"); err != nil {
		st.Manual = "niri not found"
	}
	return st
}

// niriHas reports whether caps:menu is among the active xkb options and a
// Menu bind signals Vito.
func niriHas(src string) (option, bind bool) {
	for _, l := range niriLines(src) {
		if l.path == "input/keyboard/xkb" && reOptions.MatchString(l.code) {
			for _, o := range strings.Split(reOptions.FindStringSubmatch(l.code)[1], ",") {
				if strings.TrimSpace(o) == "caps:menu" {
					option = true
				}
			}
		}
		if strings.Contains(l.code, `"-RTMIN+1"`) && strings.Contains(l.code, "vito serve") {
			bind = true
		}
	}
	return
}

var (
	reOptions = regexp.MustCompile(`^\s*options\s+"([^"]*)"`)
	reMenu    = regexp.MustCompile(`^\s*Menu(\s|\{)`)
)

type niriLine struct {
	code string // the line without a trailing // comment
	path string // the blocks it is in, "input/keyboard/xkb"
	open string // the block this line opens, if any
}

// niriLines walks the config line by line and tracks which block each line
// is in. KDL is richer than this, but niri configs keep one node per line.
func niriLines(src string) []niriLine {
	var out []niriLine
	var stack []string
	block := false
	for _, raw := range strings.Split(src, "\n") {
		code := raw
		if block {
			if i := strings.Index(code, "*/"); i >= 0 {
				code, block = code[i+2:], false
			} else {
				out = append(out, niriLine{path: strings.Join(stack, "/")})
				continue
			}
		}
		code = stripComment(code)
		if i := strings.Index(code, "/*"); i >= 0 && !strings.Contains(code[i:], "*/") {
			code, block = code[:i], true
		}
		l := niriLine{code: code, path: strings.Join(stack, "/")}
		opens, closes := strings.Count(code, "{"), strings.Count(code, "}")
		if opens > closes {
			name := strings.Fields(strings.TrimSpace(code))
			n := ""
			if len(name) > 0 {
				n = strings.TrimSuffix(name[0], "{")
			}
			stack = append(stack, n)
			l.open = n
		} else if closes > opens && len(stack) > 0 {
			stack = stack[:len(stack)-1]
		}
		out = append(out, l)
	}
	return out
}

// stripComment drops a // comment that is not inside a string.
func stripComment(s string) string {
	in := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"' && (i == 0 || s[i-1] != '\\'):
			in = !in
		case !in && s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
			return s[:i]
		}
	}
	return s
}

// SetNiriCaps adds or removes Caps Lock as the dictation key in the niri config.
func SetNiriCaps(on bool) error {
	if !niriSession() {
		return fmt.Errorf("not a niri session")
	}
	if _, err := exec.LookPath("niri"); err != nil {
		return fmt.Errorf("%w: niri is not on the PATH, so Vito cannot check the result", ErrNiriManual)
	}
	path := niriConfigPath()
	// A config kept elsewhere (a dotfiles repository) is changed where it is.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNiriManual, err)
	}
	src := string(b)
	if opt, bind := niriHas(src); opt == on && bind == on {
		return nil
	}
	out, err := niriEdit(src, on)
	if err != nil {
		return err
	}
	// niri checks it beside the real file, so its includes resolve the same.
	check := filepath.Join(filepath.Dir(path), ".vito-check-config.kdl")
	if err := os.WriteFile(check, []byte(out), 0o600); err != nil {
		return fmt.Errorf("%w: %v", ErrNiriManual, err)
	}
	defer os.Remove(check)
	if msg, err := exec.Command("niri", "validate", "-c", check).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: niri does not accept the change: %s", ErrNiriManual, lastLine(msg))
	}
	backup := path + ".vito-" + time.Now().Format("20060102-150405") + ".bak"
	if err := os.WriteFile(backup, b, 0o600); err != nil {
		return fmt.Errorf("%w: no copy of the config could be kept: %v", ErrNiriManual, err)
	}
	info, _ := os.Stat(path)
	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".vito-new"
	if err := os.WriteFile(tmp, []byte(out), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// lastLine picks niri's reason out of its report: the last line marked ×.
func lastLine(b []byte) string {
	// niri colours its output; strip the escape sequences.
	text := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(string(bytes.ReplaceAll(b, []byte("\r"), nil)), "")
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if _, after, ok := strings.Cut(lines[i], "×"); ok {
			return strings.TrimSpace(after)
		}
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// niriEdit returns src with Caps Lock set up (on) or taken out.
func niriEdit(src string, on bool) (string, error) {
	lines := strings.Split(src, "\n")
	parsed := niriLines(src)

	// Vito's own binds block: the marker comment and the block after it.
	ours := map[int]bool{}
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), niriMarker) {
			continue
		}
		ours[i] = true
		if i+1 < len(lines) && parsed[i+1].open != "binds" {
			// Inside the user's binds block: up to the end marker.
			for j := i + 1; j < len(lines); j++ {
				ours[j] = true
				if strings.HasPrefix(strings.TrimSpace(lines[j]), niriEnd) {
					break
				}
			}
			continue
		}
		// A binds block of Vito's own, as an earlier version wrote it.
		depth, opened := 0, false
		for j := i + 1; j < len(lines); j++ {
			ours[j] = true
			c := parsed[j].code
			depth += strings.Count(c, "{") - strings.Count(c, "}")
			if strings.Contains(c, "{") {
				opened = true
			}
			if opened && depth <= 0 {
				break
			}
		}
	}
	if on {
		for i, l := range parsed {
			if !ours[i] && reMenu.MatchString(l.code) && strings.HasPrefix(l.path, "binds") {
				return "", fmt.Errorf("%w: Menu is already bound in your niri config", ErrNiriManual)
			}
		}
	}

	// The xkb options.
	optLine, xkbLine, kbLine, inputLine, bindsLine := -1, -1, -1, -1, -1
	for i, l := range parsed {
		switch {
		case l.open == "binds" && l.path == "" && !ours[i] && bindsLine < 0:
			bindsLine = i
		case l.path == "input/keyboard/xkb" && reOptions.MatchString(l.code) && optLine < 0:
			optLine = i
		case l.open == "xkb" && l.path == "input/keyboard" && xkbLine < 0:
			xkbLine = i
		case l.open == "keyboard" && l.path == "input" && kbLine < 0:
			kbLine = i
		case l.open == "input" && l.path == "" && inputLine < 0:
			inputLine = i
		}
	}
	indent := func(i int) string { return lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))] }
	insert := map[int][]string{} // lines to add after line i
	switch {
	case optLine >= 0:
		m := reOptions.FindStringSubmatchIndex(lines[optLine])
		cur := lines[optLine][m[2]:m[3]]
		next := niriOptions(cur, on)
		lines[optLine] = lines[optLine][:m[2]] + next + lines[optLine][m[3]:]
	case !on:
	case xkbLine >= 0:
		insert[xkbLine] = []string{indent(xkbLine) + `    options "caps:menu"`}
	case kbLine >= 0:
		in := indent(kbLine) + "    "
		insert[kbLine] = []string{in + "xkb {", in + `    options "caps:menu"`, in + "}"}
	case inputLine >= 0:
		in := indent(inputLine) + "    "
		insert[inputLine] = []string{in + "keyboard {", in + "    xkb {", in + `        options "caps:menu"`, in + "    }", in + "}"}
	default:
		insert[len(lines)-1] = []string{"", "input {", "    keyboard {", "        xkb {", `            options "caps:menu"`, "        }", "    }", "}"}
	}

	if on && bindsLine >= 0 {
		in := indent(bindsLine) + "    "
		for _, l := range niriBind {
			insert[bindsLine] = append(insert[bindsLine], in+l)
		}
	}
	var out []string
	for i, l := range lines {
		if ours[i] {
			continue
		}
		out = append(out, l)
		out = append(out, insert[i]...)
	}
	text := strings.Join(out, "\n")
	// Removing the block leaves the blank line before it.
	text = strings.TrimRight(text, "\n") + "\n"
	if on && bindsLine < 0 {
		text += "\nbinds {\n"
		for _, l := range niriBind {
			text += "    " + l + "\n"
		}
		text += "}\n"
	}
	return text, nil
}

// niriOptions sets or clears caps:menu in an xkb option list. Turned on, it
// replaces whatever else Caps Lock was made to do.
func niriOptions(list string, on bool) string {
	var out []string
	for _, o := range strings.Split(list, ",") {
		o = strings.TrimSpace(o)
		if o == "" || o == "caps:menu" {
			continue
		}
		if on && (strings.HasPrefix(o, "caps:") || o == "ctrl:nocaps" || o == "ctrl:swapcaps") {
			continue
		}
		out = append(out, o)
	}
	if on {
		out = append(out, "caps:menu")
	}
	return strings.Join(out, ",")
}
