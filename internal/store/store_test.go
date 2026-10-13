package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileSecretsAndConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := FileSecrets{Path: filepath.Join(ConfigDir(), "credentials.json")}
	if _, err := s.Get("p"); err != ErrNoSecret {
		t.Fatalf("empty Get err = %v", err)
	}
	want := Secret{Token: "xoxp-1", Cookie: `d=a"b\c; d-s=1`}
	if err := s.Set("p", want); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("p"); got != want {
		t.Fatalf("got %+v", got)
	}
	if fi, _ := os.Stat(s.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials perm = %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(ConfigDir()); fi.Mode().Perm() != 0o700 {
		t.Errorf("config dir perm = %v", fi.Mode().Perm())
	}
	if err := s.Delete("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("p"); err != ErrNoSecret {
		t.Errorf("after delete err = %v", err)
	}

	c, _ := LoadConfig()
	c.Current = "p"
	c.Profiles["p"] = Profile{Kind: "user", Team: "Acme", URL: "https://acme.slack.com/"}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(ConfigDir(), "config.json"))
	if strings.Contains(string(b), "xox") {
		t.Errorf("config에 토큰이 있다: %s", b)
	}
	c2, _ := LoadConfig()
	if c2.Profiles["p"].Team != "Acme" || c2.Current != "p" {
		t.Errorf("reload = %+v", c2)
	}
}

func TestCache(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	if _, ok := LoadCache[string](c, "x", CacheTTL); ok {
		t.Fatal("cold cache hit")
	}
	SaveCache(c, "x", []string{"a"})
	if got, ok := LoadCache[string](c, "x", CacheTTL); !ok || got[0] != "a" {
		t.Fatalf("got %v %v", got, ok)
	}
	os.WriteFile(filepath.Join(c.Dir, "x.json"), []byte("{broken"), 0o600)
	if _, ok := LoadCache[string](c, "x", CacheTTL); ok {
		t.Error("corrupt cache hit")
	}
	old := `{"fetched_at":"` + time.Now().Add(-25*time.Hour).Format(time.RFC3339) + `","items":["a"]}`
	os.WriteFile(filepath.Join(c.Dir, "x.json"), []byte(old), 0o600)
	if _, ok := LoadCache[string](c, "x", CacheTTL); ok {
		t.Error("expired cache hit")
	}
	if _, ok := LoadCache[string](c, "x", 0); !ok {
		t.Error("maxAge 0인데 만료 처리")
	}
}

// 실제 login 키체인에 쓰므로 opt-in: GSLACK_KEYCHAIN_TEST=1 go test ./internal/store/
func TestKeychain(t *testing.T) {
	if os.Getenv("GSLACK_KEYCHAIN_TEST") != "1" {
		t.Skip("GSLACK_KEYCHAIN_TEST=1 일 때만")
	}
	k := Keychain{Service: "gslack-test"}
	defer k.Delete(`pro"file`)
	want := Secret{Token: "xoxp-a\"b\\c $x `y`", Cookie: "d=xoxd-1%2B; d-s=9"}
	if err := k.Set(`pro"file`, want); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get(`pro"file`); err != nil || got != want {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := k.Set("x", Secret{Token: "한글"}); err == nil {
		t.Error("non-ASCII accepted")
	}
	k.Delete(`pro"file`)
	if _, err := k.Get(`pro"file`); err != ErrNoSecret {
		t.Errorf("after delete err = %v", err)
	}
}
