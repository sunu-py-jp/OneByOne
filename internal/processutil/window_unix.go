//go:build !windows

package processutil

import "os/exec"

// HideWindow is unnecessary on systems that do not open a console for children.
func HideWindow(cmd *exec.Cmd) {}
