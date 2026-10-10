//go:build !amd64

package wakeword

func dot(a, b []float32) float32 { return dotGo(a, b) }
