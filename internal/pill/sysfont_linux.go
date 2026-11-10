package pill

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// systemFontFiles is the desktop's interface font, asked of GNOME, Cinnamon
// or KDE, else fontconfig's sans-serif; fc-match finds each weight's file.
func systemFontFiles() (regular, semibold, bold fontFile) {
	family := desktopFontFamily()
	return fcMatch(family, "regular"), fcMatch(family, "semibold"), fcMatch(family, "bold")
}

func sysRun(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// desktopFontFamily is the family of the desktop's interface font, or
// "sans-serif".
func desktopFontFamily() string {
	// KDE first on KDE: gsettings answers there too, with GNOME's default.
	if strings.Contains(os.Getenv("XDG_CURRENT_DESKTOP"), "KDE") {
		if v := kdeFont(); v != "" {
			return v
		}
	}
	for _, schema := range []string{"org.gnome.desktop.interface", "org.cinnamon.desktop.interface"} {
		// 'Adwaita Sans 11' → Adwaita Sans
		if v := strings.Trim(sysRun("gsettings", "get", schema, "font-name"), "'"); v != "" {
			if i := strings.LastIndexByte(v, ' '); i > 0 {
				if _, err := strconv.ParseFloat(v[i+1:], 64); err == nil {
					v = v[:i]
				}
			}
			return v
		}
	}
	if v := kdeFont(); v != "" {
		return v
	}
	return "sans-serif"
}

func kdeFont() string {
	for _, k := range []string{"kreadconfig6", "kreadconfig5"} {
		// Noto Sans,10,-1,5,400,0,0,0,0,0,0,0,0,0,0,1
		if v := sysRun(k, "--group", "General", "--key", "font"); v != "" {
			return strings.SplitN(v, ",", 2)[0]
		}
	}
	return ""
}

func fcMatch(family, weight string) fontFile {
	out := sysRun("fc-match", "-f", "%{file}\n%{index}", family+":weight="+weight)
	path, idx, _ := strings.Cut(out, "\n")
	n, _ := strconv.Atoi(idx)
	// A variable font's named instance is in the upper bits; x/image draws
	// only the default one, from the face in the lower.
	return fontFile{path: path, index: n & 0xFFFF}
}
