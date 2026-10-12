package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const CacheTTL = 24 * time.Hour

// Cache는 프로필별 이름 캐시 디렉터리다(~/.cache/gslack/<profile>/).
type Cache struct{ Dir string }

func OpenCache(profile string) Cache { return Cache{Dir: filepath.Join(CacheDir(), profile)} }

var profileNameRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// ValidProfileName: 프로필 이름은 캐시 경로가 되고 logout이 그 경로를 지운다 — 경로 문자를 막는다.
func ValidProfileName(name string) bool { return profileNameRe.MatchString(name) }

type cacheFile[T any] struct {
	FetchedAt time.Time `json:"fetched_at"`
	Items     []T       `json:"items"`
}

// LoadCache는 없거나, TTL이 지났거나, 손상된 캐시를 miss(ok=false)로 본다.
func LoadCache[T any](c Cache, name string) (items []T, ok bool) {
	b, err := os.ReadFile(filepath.Join(c.Dir, name+".json"))
	if err != nil {
		return nil, false
	}
	var f cacheFile[T]
	if json.Unmarshal(b, &f) != nil || time.Since(f.FetchedAt) > CacheTTL {
		return nil, false
	}
	return f.Items, true
}

func SaveCache[T any](c Cache, name string, items []T) error {
	b, err := json.Marshal(cacheFile[T]{FetchedAt: time.Now(), Items: items})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(c.Dir, name+".json"), b)
}

func (c Cache) Clear() error {
	if filepath.Dir(c.Dir) != filepath.Clean(CacheDir()) { // 캐시 루트 바로 아래만 지운다
		return fmt.Errorf("캐시 경로가 아니다: %s", c.Dir)
	}
	return os.RemoveAll(c.Dir)
}
