//go:build windows

package engine

import (
	"os/exec"
	"strconv"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x200}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		k := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		k.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := k.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
