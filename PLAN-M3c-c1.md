# M3c c.1 구현 계획 — `gslack auth import` (Slack 데스크톱 앱에서 xoxc + d 쿠키)

> 2026-10-13 · 대상: TASKS M3c c.1 · 상태: 초안(리뷰 전)
> 목표: DevTools 손 작업(xoxc는 `localConfig_v2`, d는 쿠키)을 없애고, Slack 데스크톱 앱 로컬 저장소에서 바로 session 프로필을 만든다.
> **비공식**: Slack(Electron/Chromium) 내부 저장소 형식에 의존 → Slack 업데이트로 깨질 수 있음을 help·실패 메시지에 명시.

## 범위

- 새 명령 `gslack auth import [profile]` — 읽어서 `store.Secret{Token: xoxc, Cookie: "d=…"}`로 기존 `login`과 **같은 검증·저장 경로**를 탄다.
- **macOS 전용**(1차). 복호화 키가 macOS Keychain "Slack Safe Storage"에 있다. Linux/Windows는 키 보관 방식이 달라 범위 밖 — `runtime.GOOS != "darwin"`이면 "macOS만 지원"으로 거절.
- 읽기만: Slack 저장소는 **절대 쓰지 않는다**. 복사본으로만 작업.

## 세 조각

### 1) xoxc 토큰 — `Local Storage/leveldb`
- 경로: `~/Library/Application Support/Slack/Local Storage/leveldb`
- 키 `localConfig_v2`(실제 저장 키는 `_https://app.slack.com\x00\x01localConfig_v2`). 값은 JSON: `{"teams": {"<TID>": {"token":"xoxc-…", "name":…, "url":…, "user_id":…}, …}}`.
- **잠금 문제**: Slack 실행 중이면 leveldb에 `LOCK`이 걸려 goleveldb `OpenFile`이 실패한다 → leveldb 디렉터리를 **임시 디렉터리로 복사**한 뒤 read-only로 연다. 복사본은 끝나면 지운다(`defer os.RemoveAll`).
- 의존성: `github.com/syndtr/goleveldb/leveldb`(순수 Go, CGO 불필요 — goreleaser `CGO_ENABLED=0` 유지). 이게 c.1에서 **유일하게 새로 추가하는 의존성**.
  - *대안(검토용)*: 의존성 없이 `.ldb`/`.log`에서 `xoxc-…`를 정규식으로 긁기. → **안 한다**: 멀티 워크스페이스(Acme 264개 대화, 팀 여러 개)에서 어느 토큰인지·compaction으로 중복/구값이 섞인다. 팀을 골라야 하므로 JSON을 제대로 파싱한다.

### 2) d 쿠키 — `Cookies`(SQLite) + Keychain 복호화
- 경로: `~/Library/Application Support/Slack/Cookies`(또는 `Network/Cookies` — 둘 다 시도).
- SQLite `cookies` 테이블에서 `name='d'`(필요하면 `d-s`도)의 `encrypted_value`를 뽑는다.
  - **SQLite 읽기는 새 의존성 없이** 시스템 `/usr/bin/sqlite3`를 `exec.Command`로 호출(이미 `security`를 그렇게 쓴다 — secret.go). macOS엔 항상 있다. 바이너리 값은 `hex(encrypted_value)`로 받아 Go에서 디코드.
  - *대안*: 순수 Go `modernc.org/sqlite`. → 무거운 의존성. shell로 충분 → **안 쓴다**.
- **복호화**(Chromium macOS 표준, 공개 사양):
  1. Keychain에서 암호: `security find-generic-password -s "Slack Safe Storage" -w`(이미 쓰는 패턴). **이 호출이 사용자 Keychain 접근 승인 프롬프트를 띄울 수 있다** — 정상.
  2. 키: `PBKDF2-HMAC-SHA1(password, salt="saltysalt", iter=1003, keyLen=16)`.
     - 전용 라이브러리 안 쓴다. 16바이트=HMAC-SHA1 한 블록 → `crypto/hmac`+`crypto/sha1`로 1003회 반복, ~10줄. `golang.org/x/crypto` 의존성 회피.
  3. `encrypted_value`는 `v10` 3바이트 접두사 + AES-128-**CBC** 암호문. IV = 16바이트 `0x20`(스페이스). 복호화 후 PKCS#7 패딩 제거.
  4. 일부 Chromium 버전은 평문 앞에 32바이트 도메인 SHA256 해시를 붙인다 → 결과가 `xoxd-`로 시작하지 않고 앞 32바이트가 쓰레기면 잘라낸다(`xoxd-` 위치 탐색으로 견고하게).
- 결과를 `store.Secret.Cookie = "d=" + value`(기존 `cookieHeader` 재사용)로.

### 3) 조립 — 기존 경로 재사용
- leveldb에서 팀이 여러 개면: 인자로 받은 profile 이름/`--team`과 매칭, 모호하면 후보(team name/url) 나열하고 종료(1.14 `@이름` 후보 나열과 같은 UX). 하나면 그걸로.
- xoxc + "d=…"로 `newAPI(token, cookie, apiTimeout).AuthTestContext` → **실 검증**(login과 동일). 실패면 "토큰이 만료됐거나 추출 실패 — Slack 재로그인 후 다시" 류 메시지.
- 성공: `store.NewSecretStore().Set`, `cfg.Profiles[name]=profileFrom(...)`, `OpenCache(name).Clear()`, `Current=name`, `Save()` — **login 뒷부분과 동일**. 공통 로직은 login에서 함수로 빼 재사용(중복 금지).

## 파일
- `internal/cli/authimport.go`(또는 auth.go에 추가 — 길면 분리). `importCmd`, `importRun`.
- 저장소 접근·복호화는 `internal/store/slackapp_darwin.go`(빌드 태그 `//go:build darwin`) + `slackapp_other.go`(stub: "macOS만"). 이렇게 두면 Linux 빌드에서 goleveldb만 들어오고 Keychain 호출은 안 컴파일.
  - *재검토*: goleveldb는 모든 OS 빌드됨(순수 Go). darwin 태그는 Keychain/경로 때문. leveldb 파싱은 공용으로 둘 수 있다.
- login의 공통 저장 로직 → `auth.go`에 `saveProfile(name, sec, res)` 헬퍼.

## 테스트(핵심 — 라이브는 사용자가)
- **복호화 단위 테스트(결정적, 자격 증명 불필요)**: 알려진 키·평문으로 AES-128-CBC+IV(0x20)·PKCS#7·`v10` 접두사·도메인해시 접두사 왕복. PBKDF2 벡터 1개(`saltysalt`, iter 1003)로 키 생성 검증. → 복호화 로직이 핵심이고 여기서 깨지면 조용히 틀린 쿠키가 나온다.
- **leveldb 파싱**: 가짜 `localConfig_v2` JSON(팀 1개·여러 개)에서 토큰·팀 추출, 모호하면 후보.
- **라이브(사용자가 `!`로)**: `! go run . auth import` → 프로필 생성 → `go run . auth status`가 session으로 통과 → `go run . read '@나'` 1건. 분류기 때문에 **실제 추출·검증은 사용자 손으로**.

## 안 하는 것 / 한계
- Linux·Windows(키 보관 다름). 요청 오면 그때.
- Slack이 꺼져 있어야 가장 안전하지만, 복사-후-열기로 **켜진 채도 시도**. 복사 중 compaction이면 재시도 1회.
- `d-s` 쿠키: 조회엔 `d`만으로 충분(spike 0.3에서 확인). 넣을지 리뷰에서 결정.
- 토큰 로테이션 자동 감지: 안 함. 만료되면 다시 `auth import`.

## Fable 리뷰 반영(2026-10-13)
- PBKDF2 자작 → **stdlib `crypto/pbkdf2`**(Go 1.24+, go.mod 1.26). 자작 crypto·`x/crypto` 의존성 제거.
- 도메인해시: `xoxd-` 바이트 스캔이 아니라 **그대로 검사 → 아니면 정확히 32바이트 드롭 후 재검사**, 둘 다 아니면 오류.
- PKCS#7: 패드 바이트(1..16, ≤len) 검증 후 trim. 틀린 키는 여기/`xoxd-` 검사에서 깔끔히 실패.
- 빌드 태그 좁힘: 파싱·복호화는 `slackapp.go`(중립, CI 테스트 가능), Keychain·경로·leveldb·sqlite만 `_darwin`.
- login 꼬리 → `saveProfile`/`deriveProfileName`/`hostPrefix` 공유 헬퍼로 추출(중복 제거).
- `d-s` 컷(YAGNI). 임시 복사본 `os.MkdirTemp`+`0700`, 전 경로 `defer RemoveAll`.
- Cookies·leveldb 모두 **복사 후** 읽어 Slack 실행 중 LOCK/WAL 회피. compaction 시 1회 재시도.
- Keychain은 `-s "Slack Safe Storage" -w`(service만).

## 리뷰 질문(Codex·Fable) — 결정
1. sqlite를 shell `sqlite3`로(의존성 0) vs `modernc.org/sqlite`(순수 Go, 재현성↑)? macOS 전용이면 shell이 맞다고 보는데.
2. leveldb: goleveldb 의존성 vs 정규식. 멀티팀 때문에 goleveldb로 기울었는데 과한가?
3. 복호화 도메인해시 접두사 처리: `xoxd-` 탐색 vs 버전 분기. 어느 게 덜 깨지나.
4. darwin 빌드 태그 분리 범위가 맞나(leveldb 공용 / Keychain·경로만 darwin).
