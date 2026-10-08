//go:build !linux && !windows

package cloudsync

func portalPickFolder(title, start string) (string, error) { return "", errNoPortal }
