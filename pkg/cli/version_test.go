package cli

import "testing"

func TestRunVersion(t *testing.T) {
	t.Cleanup(func() { Version = "dev" })
	Version = "1.2.3"

	stdout, _, code := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("version: exit %d", code)
	}
	if stdout != "1.2.3\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "1.2.3\n")
	}
}

func TestRunVersionExtraArg(t *testing.T) {
	stdout, stderr, code := runCLI(t, "version", "extra-arg")
	if code != 1 {
		t.Fatalf("version extra-arg: exit %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Fatalf("expected an error message on stderr")
	}
}

func TestRunVersionFlag(t *testing.T) {
	t.Cleanup(func() { Version = "dev" })
	Version = "1.2.3"

	stdout, _, code := runCLI(t, "--version")
	if code != 0 {
		t.Fatalf("--version: exit %d", code)
	}
	if stdout != "1.2.3\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "1.2.3\n")
	}
}

func TestRunVersionShortFlag(t *testing.T) {
	t.Cleanup(func() { Version = "dev" })
	Version = "1.2.3"

	stdout, _, code := runCLI(t, "-v")
	if code != 0 {
		t.Fatalf("-v: exit %d", code)
	}
	if stdout != "1.2.3\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "1.2.3\n")
	}
}
