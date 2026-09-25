//go:build windows

package engine

import (
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"

	"onebyone/internal/processutil"
)

func configureProcess(cmd *exec.Cmd) {
	processutil.HideWindow(cmd)
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		k := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		processutil.HideWindow(k)
		if err := k.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
