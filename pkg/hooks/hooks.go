// Package hooks runs external scripts around every agent turn, regardless
// of which channel or API triggered it. The scripts' purpose (blinking an
// LED, playing a sound, writing a log line, ...) is entirely up to the
// script itself — this package only knows two file paths and when to run
// them.
package hooks

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// scriptTimeout bounds how long a single script invocation may run before
// it's killed — a hung script must never hang the caller.
const scriptTimeout = 5 * time.Second

// Config points to the two scripts run around every agent turn. Either
// path may be empty, which disables that half; both are empty by default,
// so souz-go behaves identically on any deployment that doesn't set them.
type Config struct {
	TurnStartScript string
	TurnEndScript   string
}

// Hooks runs the configured scripts around an agent turn.
type Hooks struct {
	cfg Config
}

// New creates a Hooks from cfg.
func New(cfg Config) *Hooks {
	return &Hooks{cfg: cfg}
}

// StartTurn runs the configured start script (if any) and returns an end
// func that runs the configured end script exactly once. Both empty paths
// are a no-op. Script failures are logged, never returned — a broken or
// missing hook script must never fail an agent turn.
func (h *Hooks) StartTurn() (end func()) {
	h.run(h.cfg.TurnStartScript)

	var once sync.Once
	return func() {
		once.Do(func() { h.run(h.cfg.TurnEndScript) })
	}
}

func (h *Hooks) run(script string) {
	if script == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
		defer cancel()
		if err := exec.CommandContext(ctx, script).Run(); err != nil {
			slog.Warn("hooks: script failed", "script", script, "error", err)
		}
	}()
}
