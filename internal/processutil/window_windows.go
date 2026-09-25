//go:build windows

package processutil

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// HideWindow prevents background console commands from opening a window.
// Keep the caller's process attributes, including cancellation-related flags.
func HideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
