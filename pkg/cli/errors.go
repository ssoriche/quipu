package cli

import "fmt"

// errf prints a "quipu: "-prefixed message to e.stderr and returns code, so
// every command reports failures the same way.
func errf(e env, code int, format string, args ...any) int {
	fmt.Fprintf(e.stderr, "quipu: "+format+"\n", args...)
	return code
}

// warnf prints a "warning: "-prefixed message to e.stderr without affecting
// the exit code (used for scan.Summary.Warnings).
func warnf(e env, format string, args ...any) {
	fmt.Fprintf(e.stderr, "warning: "+format+"\n", args...)
}
