package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Secret struct {
	Token    string `json:"token"`
	AppToken string `json:"app_token,omitempty"`
	Cookie   string `json:"cookie,omitempty"` // raw Cookie 헤더(d=…; d-s=…)
}

var ErrNoSecret = errors.New("저장된 자격 증명이 없다")

type SecretStore interface {
	Get(profile string) (Secret, error)
	Set(profile string, s Secret) error
	Delete(profile string) error
}

// NewSecretStore: macOS는 Keychain(길면 0600 파일 폴백), 그 외(또는 GSLACK_SECRET_BACKEND=file)는 0600 파일.
func NewSecretStore() SecretStore {
	file := FileSecrets{Path: filepath.Join(ConfigDir(), "credentials.json")}
	if runtime.GOOS == "darwin" && os.Getenv("GSLACK_SECRET_BACKEND") != "file" {
		return macStore{kc: Keychain{Service: "gslack"}, file: file}
	}
	return file
}

// macStore: Keychain에 넣되 security CLI의 128B getpass 한계를 넘는 긴 값(xoxc 세션 등)은
// 0600 파일로 폴백한다. 짧은 xoxp는 Keychain(암호화 at-rest) 그대로.
type macStore struct {
	kc   Keychain
	file FileSecrets
}

// kcMax: security의 getpass는 128바이트에서 잘린다 — 여유를 두고 그 아래만 Keychain에.
const kcMax = 120

func (m macStore) Set(profile string, s Secret) error {
	b, _ := json.Marshal(s)
	if len(b) > kcMax { // 길면 파일로, Keychain 잔여 제거
		if err := m.kc.Delete(profile); err != nil {
			return err
		}
		return m.file.Set(profile, s)
	}
	if err := m.file.Delete(profile); err != nil { // 짧으면 Keychain으로, 파일 잔여 제거
		return err
	}
	return m.kc.Set(profile, s)
}

func (m macStore) Get(profile string) (Secret, error) {
	s, err := m.kc.Get(profile)
	if errors.Is(err, ErrNoSecret) {
		return m.file.Get(profile)
	}
	return s, err
}

func (m macStore) Delete(profile string) error {
	if err := m.kc.Delete(profile); err != nil {
		return err
	}
	return m.file.Delete(profile)
}

// Keychain은 `security` CLI를 쓴다. 값은 argv가 아니라 stdin으로 넘긴다:
// `-w`를 값 없이 마지막에 두면 security가 stdin에서 값을 두 번(확인용) 읽는다.
type Keychain struct{ Service string }

func (k Keychain) Set(profile string, s Secret) error {
	b, _ := json.Marshal(s)
	v := string(b)
	for _, r := range v {
		// find -w는 비ASCII 값을 hex로 출력한다 — 토큰·쿠키는 ASCII뿐이어야 한다.
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("자격 증명에 출력 불가능하거나 비ASCII 문자가 있다")
		}
	}
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", k.Service, "-a", profile, "-w")
	cmd.Stdin = strings.NewReader(v + "\n" + v + "\n")
	detachTTY(cmd) // tty가 있으면 security가 stdin 대신 tty에서 읽어 빈 값이 저장된다
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain 저장 실패: %v: %s", err, bytes.TrimSpace(out))
	}
	// 입력이 어긋나면 security는 빈 값을 저장하고도 0으로 끝난다 — 읽어서 확인한다.
	if got, err := k.Get(profile); err != nil || got != s {
		return fmt.Errorf("keychain 저장 검증 실패")
	}
	return nil
}

func (k Keychain) Get(profile string) (Secret, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", k.Service, "-a", profile, "-w").Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 { // errSecItemNotFound
		return Secret{}, ErrNoSecret
	}
	if err != nil {
		return Secret{}, fmt.Errorf("keychain 읽기 실패: %w", err)
	}
	var s Secret
	if err := json.Unmarshal(bytes.TrimRight(out, "\n"), &s); err != nil {
		return Secret{}, fmt.Errorf("keychain 값이 손상됐다: %w", err)
	}
	return s, nil
}

func (k Keychain) Delete(profile string) error {
	err := exec.Command("security", "delete-generic-password", "-s", k.Service, "-a", profile).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 {
		return nil
	}
	return err
}

// FileSecrets는 프로필 → Secret 맵을 0600 JSON 파일 하나에 둔다.
type FileSecrets struct{ Path string }

func (f FileSecrets) load() (map[string]Secret, error) {
	m := map[string]Secret{}
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (f FileSecrets) save(m map[string]Secret) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeFileAtomic(f.Path, b)
}

func (f FileSecrets) Get(profile string) (Secret, error) {
	m, err := f.load()
	if err != nil {
		return Secret{}, err
	}
	s, ok := m[profile]
	if !ok {
		return Secret{}, ErrNoSecret
	}
	return s, nil
}

func (f FileSecrets) Set(profile string, s Secret) error {
	m, err := f.load()
	if err != nil {
		return err
	}
	m[profile] = s
	return f.save(m)
}

func (f FileSecrets) Delete(profile string) error {
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, profile)
	return f.save(m)
}
