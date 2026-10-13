package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"testing"
)

// encryptV10은 테스트용으로 Chromium macOS와 같은 방식으로 암호화한다.
func encryptV10(t *testing.T, plaintext []byte, password string) []byte {
	t.Helper()
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	pt := append(append([]byte(nil), plaintext...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	ct := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{' '}, aes.BlockSize)).CryptBlocks(ct, pt)
	return append([]byte("v10"), ct...)
}

func TestDecryptCookie(t *testing.T) {
	const pw, token = "s3cr3t-safe-storage", "xoxd-abc123%2Fdef"

	if got, err := decryptCookie(encryptV10(t, []byte(token), pw), pw); err != nil || got != token {
		t.Fatalf("기본: got %q err %v, want %q", got, err, token)
	}
	// 최신 Chromium: 평문 앞 32바이트 도메인 해시.
	withHash := append(bytes.Repeat([]byte{0xAB}, 32), token...)
	if got, err := decryptCookie(encryptV10(t, withHash, pw), pw); err != nil || got != token {
		t.Fatalf("도메인해시: got %q err %v, want %q", got, err, token)
	}
	// 키가 틀리면 패딩/토큰 검증에서 막힌다(쓰레기 쿠키 저장 금지).
	if _, err := decryptCookie(encryptV10(t, []byte(token), pw), "wrong"); err == nil {
		t.Fatal("키 불일치인데 오류가 없다")
	}
	// v10이 아니면 거절.
	if _, err := decryptCookie([]byte("plain"), pw); err == nil {
		t.Fatal("v10 아님인데 통과")
	}
	// 복호화는 되지만 xoxd-가 아니면 거절.
	if _, err := decryptCookie(encryptV10(t, []byte("not-a-token"), pw), pw); err == nil {
		t.Fatal("xoxd- 아님인데 통과")
	}
}

func TestDecodeLevelDBValue(t *testing.T) {
	if got := decodeLevelDBValue(append([]byte{0x01}, "hi"...)); string(got) != "hi" {
		t.Errorf("latin1: %q", got)
	}
	utf16 := []byte{0x00, 'h', 0, 'i', 0} // UTF-16LE "hi"
	if got := decodeLevelDBValue(utf16); string(got) != "hi" {
		t.Errorf("utf16: %q", got)
	}
}

func TestParseLocalConfig(t *testing.T) {
	js := `{"teams":{"T1":{"id":"T1","name":"Acme","url":"https://acme.slack.com/","token":"xoxc-1"},
		"T2":{"name":"Other","url":"https://other.slack.com/","token":"xoxc-2"},
		"T3":{"name":"NoToken","url":"https://x.slack.com/"}}}`
	teams, err := parseLocalConfig([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 2 { // 토큰 없는 T3은 제외
		t.Fatalf("팀 %d개, want 2: %+v", len(teams), teams)
	}
	if _, err := parseLocalConfig([]byte(`{"teams":{}}`)); err == nil {
		t.Fatal("빈 팀인데 오류 없음")
	}
}
