//go:build !windows && !darwin

package uitext

func osLang() string { return envLang() }
