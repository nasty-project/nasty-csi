// Package cmdexec bounds subprocess output draining and preserves cancellation.
package cmdexec

import (
	"context"
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
