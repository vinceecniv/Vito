//go:build !windows && !linux && !darwin

package frontapp

func current() App { return App{} }
