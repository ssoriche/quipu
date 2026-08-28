package claudedata

import (
	"path/filepath"
	"testing"
)

func TestSlugFor(t *testing.T) {
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{
			name: "real example",
			cwd:  "/Users/alice/work/alice.CI-3760.fdk-graceful-shutdown",
			want: "-Users-alice-work-alice-CI-3760-fdk-graceful-shutdown",
		},
		{
			name: "every non-alphanumeric byte becomes a dash",
			cwd:  "/a b_c!d/e",
			want: "-a-b-c-d-e",
		},
		{
			name: "already alphanumeric is untouched",
			cwd:  "abc123",
			want: "abc123",
		},
		{
			name: "empty string",
			cwd:  "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SlugFor(tt.cwd)
			if got != tt.want {
				t.Fatalf("SlugFor(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

func TestProjectDir(t *testing.T) {
	home := "/Users/alice"
	cwd := "/Users/alice/work/alice.CI-3760.fdk-graceful-shutdown"
	want := filepath.Join(home, ".claude", "projects", "-Users-alice-work-alice-CI-3760-fdk-graceful-shutdown")

	got := ProjectDir(home, cwd)
	if got != want {
		t.Fatalf("ProjectDir(%q, %q) = %q, want %q", home, cwd, got, want)
	}
}
