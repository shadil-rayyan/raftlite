//go:build !windows

package measure

import "os/exec"

// setupProc is a no-op on non-Windows platforms (components share the
// parent's process group semantics).
func setupProc(cmd *exec.Cmd) {}
