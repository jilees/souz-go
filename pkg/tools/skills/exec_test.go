package skills

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBashBinary_PrefersBashWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if got := bashBinary(); got != "bash" {
		t.Errorf("bashBinary() = %q, want %q", got, "bash")
	}
}

// TestBashBinary_FallsBackToShWhenBashMissing scopes PATH to a directory
// containing only sh (via a symlink to the real one), simulating an
// embedded target like SberBoom Home that ships BusyBox sh but no bash.
func TestBashBinary_FallsBackToShWhenBashMissing(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	dir := t.TempDir()
	if err := os.Symlink(sh, filepath.Join(dir, "sh")); err != nil {
		t.Fatalf("symlink sh: %v", err)
	}
	t.Setenv("PATH", dir)

	if _, err := exec.LookPath("bash"); err == nil {
		t.Fatal("test setup broken: bash still resolves with the scoped PATH")
	}

	if got := bashBinary(); got != "sh" {
		t.Errorf("bashBinary() = %q, want %q", got, "sh")
	}

	spec, err := buildRunSpec(runtimeBash, "echo hi", nil, t.TempDir())
	if err != nil {
		t.Fatalf("buildRunSpec: %v", err)
	}
	if spec.Name != "sh" {
		t.Errorf("buildRunSpec interpreter = %q, want %q", spec.Name, "sh")
	}
}
