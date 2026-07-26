package daemon

import "syscall"

// SysProcAttrForBackground returns SysProcAttr settings appropriate for
// detaching a child process from the parent's controlling terminal.
// macOS / Linux only.
func SysProcAttrForBackground() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true,
	}
}
