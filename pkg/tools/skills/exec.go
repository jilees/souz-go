package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// runtime selects how a skill command is executed.
type runtime string

const (
	runtimeBash    runtime = "bash"
	runtimePython  runtime = "python"
	runtimeNode    runtime = "node"
	runtimeProcess runtime = "process"
)

// runSpec is everything needed to launch one subprocess.
type runSpec struct {
	Name    string // interpreter/binary to exec.LookPath
	Args    []string
	Dir     string
	Env     []string
	Stdin   string
	cleanup func()
}

// buildRunSpec resolves runtime/script/command into a concrete command line.
// For bash/python/node, script is written to a temp file inside dir so the
// interpreter gets a real filename (for stack traces and any relative
// imports) rather than relying on stdin-as-script quirks that differ
// between interpreters.
func buildRunSpec(rt runtime, script string, command []string, dir string) (runSpec, error) {
	switch rt {
	case runtimeBash, "":
		return scriptRunSpec(bashBinary(), ".sh", script, dir)
	case runtimePython:
		return scriptRunSpec(pythonBinary(), ".py", script, dir)
	case runtimeNode:
		return scriptRunSpec("node", ".js", script, dir)
	case runtimeProcess:
		if len(command) == 0 {
			return runSpec{}, fmt.Errorf("runtime %q requires a non-empty command", rt)
		}
		return runSpec{Name: command[0], Args: command[1:], Dir: dir}, nil
	default:
		return runSpec{}, fmt.Errorf("unknown runtime %q", rt)
	}
}

func pythonBinary() string {
	if _, err := exec.LookPath("python3"); err == nil {
		return "python3"
	}
	return "python"
}

// bashBinary falls back to sh when bash isn't installed — true of BusyBox
// embedded targets like SberBoom Home, which ship only /bin/sh (ash), no
// bash. Scripts relying on bash-only syntax (arrays, [[ ]], process
// substitution, ...) will fail under sh; that's a real behavior difference
// skill authors need to write around on such targets, not something this
// fallback can paper over — it only saves runtime "bash" from failing
// outright with "bash is not available on this system" everywhere sh would
// have worked fine.
func bashBinary() string {
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash"
	}
	return "sh"
}

func scriptRunSpec(interpreter, ext, script, dir string) (runSpec, error) {
	if strings.TrimSpace(script) == "" {
		return runSpec{}, fmt.Errorf("script is required for this runtime")
	}
	f, err := os.CreateTemp(dir, ".skill-script-*"+ext)
	if err != nil {
		return runSpec{}, fmt.Errorf("write script: %w", err)
	}
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		os.Remove(f.Name())
		return runSpec{}, fmt.Errorf("write script: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return runSpec{}, fmt.Errorf("write script: %w", err)
	}
	path := f.Name()
	return runSpec{
		Name:    interpreter,
		Args:    []string{path},
		Dir:     dir,
		cleanup: func() { os.Remove(path) },
	}, nil
}

type runResult struct {
	ExitCode int
	TimedOut bool
	Stdout   string
	Stderr   string
}

// run executes spec, capturing output up to maxCapturedBytes per stream
// (further output is silently discarded, not an error, so a runaway
// process can't exhaust memory on a 256MB device) and honoring ctx's
// deadline as the command timeout.
func run(ctx context.Context, spec runSpec) (runResult, error) {
	if spec.cleanup != nil {
		defer spec.cleanup()
	}

	if _, err := exec.LookPath(spec.Name); err != nil {
		return runResult{}, fmt.Errorf("%q is not available on this system: %w", spec.Name, err)
	}

	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}
	// A script can spawn children (e.g. bash running "sleep 5") that
	// inherit its stdout/stderr pipes. Killing just the direct child on
	// timeout leaves those orphans holding the pipes open, so Wait() would
	// block on their EOF instead of the timeout. Run the tree in its own
	// process group and kill the whole group, with WaitDelay as a backstop
	// that force-closes the pipes regardless.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	var stdout, stderr cappedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := runResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() != nil {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return runResult{}, err
}

// cappedBuffer discards writes past maxCapturedBytes instead of growing
// without bound, while still reporting success to the writer (a child
// process that gets a write error on stdout/stderr may behave badly).
type cappedBuffer struct {
	buf bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := maxCapturedBytes - c.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			c.buf.Write(p[:remaining])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
