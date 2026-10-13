//go:build darwin

package store

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// slackAppDir은 Slack 데스크톱 앱의 Application Support 경로다.
func slackAppDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "Application Support", "Slack")
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("Slack 데스크톱 앱 데이터를 찾지 못했다(%s)", dir)
	}
	return dir, nil
}

// ImportSlackApp은 Slack 앱에서 로그인된 팀들과 d 쿠키 값(xoxd-)을 읽는다. 읽기만 한다.
func ImportSlackApp() (teams []SlackTeam, dCookie string, err error) {
	base, err := slackAppDir()
	if err != nil {
		return nil, "", err
	}
	tmp, err := os.MkdirTemp("", "gslack-import-") // 토큰·쿠키가 잠깐 지나간다(MkdirTemp는 0700)
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)

	teams, err = readTeams(filepath.Join(base, "Local Storage", "leveldb"), tmp)
	if err != nil {
		return nil, "", err
	}
	password, err := safeStoragePassword()
	if err != nil {
		return nil, "", err
	}
	dCookie, err = readDCookie(base, tmp, password)
	if err != nil {
		return nil, "", err
	}
	return teams, dCookie, nil
}

// readTeams는 leveldb를 임시로 복사해 열고(Slack 실행 중 LOCK 회피) localConfig_v2를 읽는다.
func readTeams(src, tmp string) ([]SlackTeam, error) {
	dst := filepath.Join(tmp, "leveldb")
	if err := copyDir(src, dst); err != nil {
		return nil, fmt.Errorf("leveldb 복사 실패: %w", err)
	}
	db, err := leveldb.OpenFile(dst, &opt.Options{ReadOnly: true})
	if err != nil {
		// compaction 중 복사면 한 번 다시.
		if err2 := copyDir(src, dst); err2 == nil {
			db, err = leveldb.OpenFile(dst, &opt.Options{ReadOnly: true})
		}
		if err != nil {
			return nil, fmt.Errorf("leveldb 열기 실패: %w", err)
		}
	}
	defer db.Close()

	var value []byte
	iter := db.NewIterator(nil, nil)
	for iter.Next() {
		if bytes.HasSuffix(iter.Key(), []byte("localConfig_v2")) {
			value = decodeLevelDBValue(append([]byte(nil), iter.Value()...))
			break
		}
	}
	iter.Release()
	if err := iter.Error(); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("leveldb에 localConfig_v2가 없다(Slack에 로그인돼 있나?)")
	}
	return parseLocalConfig(value)
}

// readDCookie는 Cookies(SQLite)에서 d 쿠키 encrypted_value를 뽑아 복호화한다.
func readDCookie(base, tmp, password string) (string, error) {
	var cookiesPath string
	for _, p := range []string{filepath.Join(base, "Cookies"), filepath.Join(base, "Network", "Cookies")} {
		if _, err := os.Stat(p); err == nil {
			cookiesPath = p
			break
		}
	}
	if cookiesPath == "" {
		return "", errors.New("Slack Cookies 파일을 찾지 못했다")
	}
	dst := filepath.Join(tmp, "Cookies") // WAL 잠금 회피용 복사본
	if err := copyFile(cookiesPath, dst); err != nil {
		return "", fmt.Errorf("Cookies 복사 실패: %w", err)
	}
	out, err := exec.Command("/usr/bin/sqlite3", "-readonly", dst,
		"select hex(encrypted_value) from cookies where name='d' order by length(encrypted_value) desc limit 1;").Output()
	if err != nil {
		return "", fmt.Errorf("sqlite3로 쿠키 읽기 실패: %w", err)
	}
	hexVal := strings.TrimSpace(string(out))
	if hexVal == "" {
		return "", errors.New("Cookies에 d 쿠키가 없다(Slack에 로그인돼 있나?)")
	}
	enc, err := hex.DecodeString(hexVal)
	if err != nil {
		return "", fmt.Errorf("쿠키 hex 디코드 실패: %w", err)
	}
	return decryptCookie(enc, password)
}

// safeStoragePassword는 Keychain의 "Slack Safe Storage" 암호를 읽는다(승인 프롬프트가 뜰 수 있다).
func safeStoragePassword() (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", "Slack Safe Storage", "-w").Output()
	if err != nil {
		return "", fmt.Errorf("Keychain에서 'Slack Safe Storage'를 읽지 못했다: %w", err)
	}
	pw := strings.TrimRight(string(out), "\n")
	if pw == "" {
		return "", errors.New("'Slack Safe Storage' 암호가 비어 있다")
	}
	return pw, nil
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue // Local Storage leveldb는 평평하다
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
