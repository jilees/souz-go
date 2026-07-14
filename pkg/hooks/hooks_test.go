package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartTurn_EmptyConfig_NoOp(t *testing.T) {
	h := New(Config{})
	end := h.StartTurn()
	end()
	end() // must be safe to call more than once
}

func TestStartTurn_RunsStartAndEndScripts(t *testing.T) {
	dir := t.TempDir()
	startMarker := filepath.Join(dir, "start.marker")
	endMarker := filepath.Join(dir, "end.marker")

	startScript := writeMarkerScript(t, dir, "start.sh", startMarker)
	endScript := writeMarkerScript(t, dir, "end.sh", endMarker)

	h := New(Config{TurnStartScript: startScript, TurnEndScript: endScript})

	end := h.StartTurn()
	waitForFile(t, startMarker)
	if fileExists(endMarker) {
		t.Fatal("end script ran before end() was called")
	}

	end()
	waitForFile(t, endMarker)

	// end must be idempotent.
	end()
}

func writeMarkerScript(t *testing.T, dir, name, markerPath string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/bin/sh\ntouch '" + markerPath + "'\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fileExists(path) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
