//go:build windows

package store

import "os/exec"

func detachTTY(*exec.Cmd) {} // Keychain은 macOS만 — windows에선 쓰지 않는다
