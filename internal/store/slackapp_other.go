//go:build !darwin

package store

import "errors"

// ImportSlackApp은 macOS에서만 동작한다(복호화 키가 macOS Keychain에 있다).
func ImportSlackApp() ([]SlackTeam, string, error) {
	return nil, "", errors.New("auth import은 macOS만 지원한다")
}
