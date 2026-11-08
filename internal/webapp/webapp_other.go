//go:build !linux && !windows && !darwin

package webapp

func storeGlobs() []string         { return nil }
func launcher(string) func() error { return nil }

func focusApp(string) bool { return false }
