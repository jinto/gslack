// Package store는 디스크에 닿는 모든 것이다: 프로필 설정, 자격 증명, 이름 캐시.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Profile에는 비밀값을 절대 두지 않는다 — 토큰은 SecretStore에.
type Profile struct {
	Kind   string `json:"kind"` // user(xoxp) | session(xoxc)
	Team   string `json:"team"`
	TeamID string `json:"team_id"`
	User   string `json:"user"`
	UserID string `json:"user_id"`
	URL    string `json:"url"`
}

type Config struct {
	Current  string             `json:"current,omitempty"`
	Profiles map[string]Profile `json:"profiles"`
}

func ConfigDir() string { return xdg("XDG_CONFIG_HOME", ".config") }
func CacheDir() string  { return xdg("XDG_CACHE_HOME", ".cache") }

func xdg(env, fallback string) string {
	if d := os.Getenv(env); d != "" {
		return filepath.Join(d, "gslack")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback, "gslack")
}

func LoadConfig() (*Config, error) {
	c := &Config{Profiles: map[string]Profile{}}
	b, err := os.ReadFile(filepath.Join(ConfigDir(), "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	return c, nil
}

func (c *Config) Save() error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(ConfigDir(), "config.json"), b)
}

// writeFileAtomic은 디렉터리 0700, 파일 0600으로 temp+rename 쓰기를 한다.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path) // CreateTemp는 0600으로 만든다
}
