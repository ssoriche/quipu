package cli

import "fmt"

// Version is quipu's build version. cmd/quipu/main.go sets it from the
// version var it declares at package scope, which release tooling injects
// via `-ldflags "-X main.version=..."`. It stays "dev" for local/test
// builds that never call the setter.
var Version = "dev"

// runVersion implements `quipu version`: prints Version and returns 0. It
// takes no arguments.
func runVersion(e env, args []string) int {
	if len(args) > 0 {
		return errf(e, 1, "version takes no arguments")
	}
	_, _ = fmt.Fprintln(e.stdout, Version)
	return 0
}
