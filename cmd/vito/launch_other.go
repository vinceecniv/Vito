//go:build !darwin

package main

// defaultCommand is the subcommand assumed when none is given. Everywhere but
// macOS, running Vito with no arguments means the user typed `vito` in a
// terminal and wants to see what it can do.
func defaultCommand() string { return "help" }

// isLaunchArg reports whether an argument is launcher noise to be skipped.
// Only macOS produces any.
func isLaunchArg(string) bool { return false }
