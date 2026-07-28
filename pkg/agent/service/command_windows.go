//go:build windows

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// PowerShell often launches native commands as child processes. taskkill /T
		// terminates the whole tree so cancellation and timeouts do not orphan them.
		if err := exec.Command("taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run(); err == nil {
			return nil
		}
		err := cmd.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
	return cmd
}
