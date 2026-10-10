package wakeword

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// MicInUseByOthers reports whether another program has the microphone open —
// a call or meeting, where "Vito" may well be said without meaning Vito.
//
// Windows records every program's microphone use for its privacy indicator,
// under CapabilityAccessManager\ConsentStore\microphone: a desktop program
// (NonPackaged, keyed by its path with \ as #) or a store app (keyed by its
// package name) is using it while its LastUsedTimeStop is 0 and its
// LastUsedTimeStart is not.
func MicInUseByOthers() bool {
	const base = `Software\Microsoft\Windows\CurrentVersion\CapabilityAccessManager\ConsentStore\microphone`
	self := ""
	if exe, err := os.Executable(); err == nil {
		self = strings.ToLower(filepath.Base(exe))
	}
	inUse := func(path string) bool {
		k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
		if err != nil {
			return false
		}
		defer k.Close()
		start, _, err1 := k.GetIntegerValue("LastUsedTimeStart")
		stop, _, err2 := k.GetIntegerValue("LastUsedTimeStop")
		return err1 == nil && err2 == nil && start != 0 && stop == 0
	}
	scan := func(path string, desktop bool) bool {
		k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			return false
		}
		names, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, n := range names {
			if !desktop && n == "NonPackaged" {
				continue
			}
			if desktop && self != "" && strings.HasSuffix(strings.ToLower(n), "#"+self) {
				continue // that's us
			}
			if inUse(path + `\` + n) {
				return true
			}
		}
		return false
	}
	return scan(base+`\NonPackaged`, true) || scan(base, false)
}
