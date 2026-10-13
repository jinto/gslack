//go:build !windows

package store

import (
	"os/exec"
	"syscall"
)

// detachTTY는 자식에서 제어 터미널을 뗀다. security의 `-w`(빈 값)는 /dev/tty에서
// getpass로 읽어 우리가 넘긴 stdin을 무시한다 — tty를 없애면 stdin으로 폴백한다.
func detachTTY(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
