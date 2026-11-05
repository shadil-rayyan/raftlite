//go:build windows

package measure

import (
	"os/exec"
	"syscall"
)

// setupProc hides the console window of spawned components on Windows so
// experiments do not open a cascade of terminal popups.
func setupProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
