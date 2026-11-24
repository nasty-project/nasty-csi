// Package cmdexec bounds subprocess output draining and preserves cancellation.
package cmdexec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Cmd embeds standard command configuration with context-aware execution.
type Cmd struct {
	*exec.Cmd
	ctx context.Context
}

// ExitError preserves standard exit-status classification for callers.
type ExitError = exec.ExitError

// ErrWaitDelay indicates that a helper retained the command's output pipes.
var ErrWaitDelay = exec.ErrWaitDelay

// CommandContext bounds waits on pipes inherited by background mount helpers.
// It does not impose a new execution timeout; the caller owns that policy.
func CommandContext(ctx context.Context, name string, args ...string) *Cmd {
	command := exec.CommandContext(ctx, name, args...)
	command.WaitDelay = 5 * time.Second
	return &Cmd{Cmd: command, ctx: ctx}
}

// CombinedOutput preserves both the exit/pipe error and cancellation cause.
func (c *Cmd) CombinedOutput() ([]byte, error) {
	output, err := c.Cmd.CombinedOutput()
	return output, c.outcome(err)
}

// Run preserves cancellation without discarding ordinary exit status.
func (c *Cmd) Run() error {
	return c.outcome(c.Cmd.Run())
}

func (c *Cmd) outcome(err error) error {
	if err != nil && c.ctx.Err() != nil {
		return fmt.Errorf("command interrupted: %w: %w", c.ctx.Err(), err)
	}
	return err
}

// IsTimeout reports whether err came from a command killed for exceeding its deadline.
// A protected command left running past its deadline (IsStillRunning) was not killed and
// is not a timeout in this sense.
func IsTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) && !IsStillRunning(err)
}

// ExitCode returns the exit code of a command that ran to completion and exited
// non-zero. It reports false for commands that were killed (timeout, cancellation,
// signal) or never started, so callers cannot mistake a killed probe for an answer.
func ExitCode(err error) (int, bool) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return 0, false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode(), true
	}
	return 0, false
}
