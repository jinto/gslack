package store

// Slack 데스크톱 앱(Electron/Chromium)의 로컬 저장소에서 session 자격 증명을 읽는다.
// 비공식: 내부 형식에 의존 → Slack 업데이트로 깨질 수 있다.
// 이 파일은 플랫폼 중립(순수 계산) — 경로·Keychain은 slackapp_darwin.go.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// SlackTeam은 localConfig_v2의 한 워크스페이스다.
type SlackTeam struct {
	ID    string
	Name  string
	URL   string
	Token string // xoxc-…
}

// parseLocalConfig는 leveldb `localConfig_v2` 값(JSON)에서 토큰이 있는 팀을 뽑는다.
func parseLocalConfig(value []byte) ([]SlackTeam, error) {
	var cfg struct {
		Teams map[string]struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			URL  string `json:"url"`
			Tok  string `json:"token"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(value, &cfg); err != nil {
		return nil, fmt.Errorf("localConfig_v2 파싱 실패: %w", err)
	}
	var teams []SlackTeam
	for k, t := range cfg.Teams {
		if t.Tok == "" {
			continue
		}
		id := t.ID
		if id == "" {
			id = k
		}
		teams = append(teams, SlackTeam{ID: id, Name: t.Name, URL: t.URL, Token: t.Tok})
	}
	if len(teams) == 0 {
		return nil, errors.New("Slack 앱에서 로그인된 팀을 찾지 못했다")
	}
	return teams, nil
}

// decodeLevelDBValue는 Chromium Local Storage 값 앞의 1바이트 인코딩 표시를 푼다:
// 0x00 = UTF-16LE, 0x01 = Latin-1/바이트 그대로.
func decodeLevelDBValue(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	switch b[0] {
	case 0x00:
		u := make([]uint16, (len(b)-1)/2)
		for i := range u {
			u[i] = uint16(b[1+2*i]) | uint16(b[2+2*i])<<8
		}
		return []byte(string(utf16.Decode(u)))
	case 0x01:
		return b[1:]
	default:
		return b // 표시 없이 저장된 경우 그대로
	}
}

// decryptCookie는 Chromium macOS의 v10 encrypted_value를 Safe Storage 암호로 푼다.
// AES-128-CBC, IV=16×0x20, PBKDF2-HMAC-SHA1(salt="saltysalt", iter=1003, len=16).
func decryptCookie(encrypted []byte, password string) (string, error) {
	if len(encrypted) < 3 || string(encrypted[:3]) != "v10" {
		return "", errors.New("쿠키 형식이 v10이 아니다")
	}
	ct := encrypted[3:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", errors.New("암호문 길이가 블록 배수가 아니다")
	}
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte{' '}, aes.BlockSize)).CryptBlocks(pt, ct)
	pt, err = pkcs7Unpad(pt)
	if err != nil {
		return "", fmt.Errorf("복호화 실패(Safe Storage 키 불일치?): %w", err)
	}
	// 최신 Chromium(M130+)은 평문 앞에 32바이트 도메인 해시를 붙인다.
	if v := string(pt); strings.HasPrefix(v, "xoxd-") {
		return v, nil
	}
	if len(pt) > 32 {
		if v := string(pt[32:]); strings.HasPrefix(v, "xoxd-") {
			return v, nil
		}
	}
	return "", errors.New("복호화 결과에서 d 쿠키(xoxd-)를 찾지 못했다")
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	n := len(b)
	if n == 0 {
		return nil, errors.New("빈 평문")
	}
	pad := int(b[n-1])
	if pad == 0 || pad > aes.BlockSize || pad > n {
		return nil, errors.New("패딩이 잘못됐다")
	}
	for _, c := range b[n-pad:] {
		if int(c) != pad {
			return nil, errors.New("패딩이 잘못됐다")
		}
	}
	return b[:n-pad], nil
}
