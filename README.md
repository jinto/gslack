# gslack

터미널에서 쓰는 Slack. 읽기·보내기·스레드·검색·리액션·파일·실시간 감시를 명령 하나씩으로.

```sh
gslack unread                       # 안 읽은 DM·그룹 DM
gslack read general -n 20           # 최근 20개
gslack send @jaekwon "점심 가요?"
gslack thread <permalink>
gslack watch --notify               # DM·멘션이 오면 한 줄 + macOS 알림
gslack tui                          # 받은편지함 화면(아래 TUI)
```

## 빠른 시작(macOS)

```sh
brew install jinto/tap/gslack
gslack auth import     # Slack 데스크톱 앱에 로그인된 계정으로(처음 한 번 Keychain 허락 창)
gslack tui
```

업데이트는 `brew upgrade gslack`. `auth import`는 비공식 방식이다 — 회사라면 보안 정책을 먼저 확인하고, 공식 방식은 아래 [Slack 앱 만들기](#slack-앱-만들기한-번)·[로그인](#로그인).

## 설치

```sh
brew install jinto/tap/gslack                # macOS·Linux
go install github.com/jinto/gslack@latest    # 또는 Go 1.26+
```

또는 [Releases](https://github.com/jinto/gslack/releases)에서 macOS·Linux 바이너리(amd64·arm64). 브라우저로 받은 macOS 바이너리는 서명이 없어 Gatekeeper가 막는다 — `xattr -d com.apple.quarantine gslack`.

## Slack 앱 만들기(한 번)

gslack은 **내 계정의 사용자 토큰(xoxp)** 으로 동작한다. 워크스페이스에 내부 앱을 하나 만든다.

1. <https://api.slack.com/apps> → **Create New App → From a manifest** → 워크스페이스 고르기 → [`slack-app-manifest.yaml`](slack-app-manifest.yaml) 붙여 넣기.
2. **Install to Workspace** → *OAuth & Permissions*의 **User OAuth Token**(`xoxp-…`).
3. (user 프로필로 watch를 쓸 때만) *Basic Information → App-Level Tokens*에서 scope `connections:write`로 토큰 생성(`xapp-…`).
4. **Manage Distribution(공개 배포)은 켜지 않는다.** 내 워크스페이스 전용 앱이다.

## 로그인

```sh
gslack auth login          # 토큰·앱 토큰을 프롬프트로(인자로 받지 않는다 — 셸 히스토리·ps에 남지 않게)
gslack auth status
gslack auth list / use <프로필> / logout
```

토큰은 macOS Keychain(그 외 OS는 `~/.config/gslack/` 0600 파일)에 둔다. 여러 워크스페이스는 프로필로, 한 번만 다른 프로필은 `--profile`.
CI처럼 저장소 없이 쓰려면 `GSLACK_TOKEN`(+ `GSLACK_APP_TOKEN`) 환경 변수.

### 브라우저 세션 토큰(xoxc) — 비공식·미검증

`xoxc-…` 토큰 + `d` 쿠키(`GSLACK_COOKIE`)도 받지만 **Slack 공식 방식이 아니다**. 회사 워크스페이스라면 먼저 보안 정책을 확인할 것.

**macOS: `auth import`로 자동 추출**

```
gslack auth import                     # Slack 앱에 로그인된 팀이 하나면 바로
gslack --profile work auth import acme # 여러 개면 [team]으로 고르고, 이름은 --profile로
```

Slack 데스크톱 앱의 로컬 저장소에서 xoxc 토큰과 `d` 쿠키를 읽어 session 프로필을 만든다(DevTools 손 작업 불필요). 처음 실행 때 Keychain "Slack Safe Storage" 접근 **승인 프롬프트**가 한 번 뜬다. 읽기만 하며 Slack 앱은 건드리지 않는다. **비공식** — Slack 내부 형식에 의존하므로 앱 업데이트로 깨질 수 있다. macOS 전용.

## TUI — `gslack tui`

위는 목록(나에게 온 것 / 안 읽은 채널), 아래는 미리보기. 실시간은 프로필 따라 자동(session은 RTM, user는 앱 토큰이 있으면 Socket Mode).

| 키 | 목록 | 미리보기(Enter로 들어감) |
|---|---|---|
| `j`/`k` | 줄 이동 | 메시지 고르기 |
| `Enter` | 미리보기 열기 | 고른 메시지의 스레드 열기 |
| `Esc` | — | 목록으로(j/k로 지나간 데까지 읽음), 연 스레드면 채널로 |
| `r` | 답장 | 고른 메시지에 답장(채널은 스레드로) |
| `m` | 대화 치우기(전부 읽음) | 같음 |
| `l` | Slack "나중에"에 넣기(session 프로필) | 고른 메시지를 |
| `o` | 브라우저로 | 고른 메시지를 |
| `R` / `q` | 새로고침 / 끝 | |

미리보기의 그림(올린 이미지·봇 첨부)은 반블록(`▀`)으로 그린다. 봇 첨부 그림은 그 서버에서 직접 받으므로 그 서버가 내 IP를 안다 — 싫으면 `GSLACK_IMAGES=off`.

## 명령

| 명령 | |
|---|---|
| `channels`, `users [검색어]`, `whois @이름` | 목록·사용자 정보 |
| `read <대상> [-n --since --before --mark]` | 대화 읽기 |
| `thread <permalink \| 대상 ts>` | 스레드 |
| `unread [--all]`, `mark <대상>` | 안 읽은 것(근사치), 읽음 처리 |
| `send <대상> [본문 \| -] [--thread ts] [--at 2h]` | 보내기·예약 |
| `edit`, `delete`, `react [--remove]` | 내 메시지 고치기·지우기, 리액션 |
| `search <검색어> [--in --from]` | 검색 |
| `upload <대상> <파일>`, `download <파일ID \| permalink>` | 파일 |
| `status [set <글> \| clear]` | 내 상태 |
| `watch [대상…] [--all] [--notify]` | 실시간(user 프로필은 앱 토큰 필요, session 프로필은 RTM으로 없이) |

**대상**: `general`(채널 이름), `#general`, `@이름`, `C…`/`D…` ID, 메시지 permalink. bash에서 `#`은 주석이라 맨 이름을 쓰는 게 편하다.
모든 명령에 `--json`(정규화된 JSON, watch는 NDJSON). 오류는 종료 코드 1(API)·2(사용법).

### watch의 한계

- 끊긴 동안 온 메시지는 다시 오지 않는다(재연결은 자동) — 빈틈은 `gslack unread`.
- 실행 중 새로 가입한 채널, 내가 답글만 단 스레드는 잡지 않는다.
- RTM(session)은 실행 중 새로 생긴 그룹 DM을 멘션 없이는 보여 주지 않고, 가입하지 않은 채널의 멘션은 오지 않는다.
- Socket Mode 연결은 내 접속 상태(presence)를 active로 만들지 않는다. RTM은 연결 중 active로 보였다.

## 셸 자동완성

```sh
source <(gslack completion zsh)    # bash·fish·powershell도. 영구 설치는 gslack completion zsh --help
```

채널 이름·`@사용자`·프로필을 완성한다. 네트워크를 쓰지 않고 이름 캐시(`~/.cache/gslack/<프로필>/`)만 읽는다 — 아무 명령이나 한 번 실행하면 채워진다. `GSLACK_TOKEN` 환경 변수 모드에서는 완성하지 않는다.

## 개발

```sh
git config core.hooksPath .githooks       # pre-commit: gitleaks(토큰 유출) · gofmt · go vet · go mod tidy · go test
go test ./...
goreleaser release --snapshot --clean    # darwin/linux × amd64/arm64 → dist/
```

릴리스는 `v*` 태그를 push하면 GitHub Actions가 goreleaser로 올린다. 기획·계획 문서는 [`docs/`](docs/)(PRD, TASKS, `plans/`, 버리는 `spike/`).

## 라이선스

MIT — [LICENSE](LICENSE).
