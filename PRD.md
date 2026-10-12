# gslack — PRD

> Slack GUI 없이 터미널에서 Slack을 쓰기 위한 Go CLI.
> 작성일: 2026-10-12 · 상태: 초안(Codex·Fable 리뷰 반영)

## 1. 배경과 목표

Slack 데스크톱 앱(Electron)은 워크스페이스마다 수백 MB의 메모리를 쓰고, 터미널에서 일하다 보면 창을 오가야 한다.

**목표**
- 데스크톱 앱을 켜지 않고도 하루 업무에 필요한 Slack 작업(읽기, 쓰기, 스레드, DM, 검색, 리액션, 파일, 상태, 실시간 알림)을 끝낸다.
- 단일 바이너리로 배포하고, 대기 중 메모리는 20MB 이하로 유지한다.
- 스크립트로 조합할 수 있게 한다. 모든 조회 명령은 `--json`을 지원하고, 입력은 stdin으로도 받는다.
- 여러 워크스페이스를 프로필로 전환해서 쓴다.

**비목표(이번 범위 밖)**
- TUI: CLI가 안정된 뒤 같은 코어 위에 별도 단계로 만든다.
- 허들/통화: 공개 API가 없다.
- Marketplace 배포, 봇 기능(슬래시 커맨드, 인터랙티브 메시지 수신).
- Canvas/Lists 편집: MVP 이후 검토한다.

**MVP 이후 백로그**: 채널 join/leave, 핀, 북마크, DND/스누즈, 리마인더, Canvas/Lists, 데스크톱 앱 토큰 자동 가져오기, `watch --all-profiles`(여러 워크스페이스 동시 감시).

## 2. 사용자

본인과 터미널 중심으로 일하는 개발자. 여러 워크스페이스(acme 포함)에 속해 있다.

## 3. 인증 — 두 방식 모두 지원

| | A. 공식 앱(User token) | B. 브라우저 세션(**비공식**) |
|---|---|---|
| 자격 증명 | `xoxp-` (+ 실시간용 `xapp-`) | `xoxc-` + `d` 쿠키(`xoxd-`) |
| 준비 | 내부 앱을 만들고 워크스페이스에 설치(관리자 승인이 필요할 수 있음) | 브라우저나 데스크톱 앱에서 값을 복사 |
| 권한 | 앱 manifest의 user scope 범위 | GUI 사용자와 동일 |
| 실시간 | Socket Mode(Events API, user 이벤트 구독) | RTM(`rtm.connect` WebSocket). 공식 문서상 새 앱은 RTM을 못 쓴다 — 세션 토큰으로만 동작 |
| 안 읽은 수 | **근사치만**. `conversations.info`의 `unread_count`는 DM에만 있고, 채널은 `last_read`가 안 올 수 있다. GUI와 같은 값을 보장하지 않는다 | 비공개 `client.counts`로 정확한 값 |
| 프레즌스 | Socket Mode 연결만으로는 active가 안 될 수 있다(계속 away로 보일 수 있음, M0에서 확인) | RTM 연결 중 active |
| 안정성 | 공식, 장기적으로 안정 | 비공식, ToS 회색지대, 로그아웃하면 만료, 무보장 |

**워크스페이스별 비용**: A는 워크스페이스마다 앱을 따로 설치해야 하고(각각 관리자 승인 필요할 수 있음), B는 워크스페이스별 `xoxc` + 공통 `d` 쿠키만 있으면 된다.

**우선순위**: A가 출시 기준이다(M0 게이트). B는 "비공식·실험적"으로 표기하고, 독립 spike로 진행해서 M0을 막지 않게 한다. 정확한 안 읽은 수와 active 프레즌스는 B 전용 기능으로 둔다. (2026-10-12 확정)

**레이트리밋 확인 결과**: 2025-05-29부터 `conversations.history/replies`는 1회/분, 15건으로 축소됐지만, 이 제한은 *Marketplace 밖에서 상업 배포되는 앱*에만 적용된다. 공식 문서는 "Internal customer-built applications are not impacted"라고 명시한다. 따라서 직접 만든 내부 앱(A)은 Tier 3(50+/분, 보장치 아님)을 유지한다. → A를 기본 읽기 경로로 써도 된다. 실제로는 M0 버스트 테스트로 확인한다.
- **규칙**: 앱 설정에서 **Distribute App(public distribution)을 절대 켜지 않는다**. 켜면 internal 판정이 깨질 수 있다.

**필요한 user scope(A)**: `channels:history groups:history im:history mpim:history channels:read groups:read im:read mpim:read channels:write groups:write im:write mpim:write chat:write reactions:read reactions:write files:read files:write search:read users:read users.profile:write users:write emoji:read`
**app-level scope**(`xapp`): `connections:write`. **user 이벤트**: `message.im message.mpim message.channels message.groups`.
→ 저장소에 `slack-app-manifest.yaml`을 넣어서 앱을 한 번에 만들 수 있게 한다.

| 명령 | 주요 메서드 | 필요 scope |
|---|---|---|
| `channels`, 캐시 | `users.conversations` | `*:read` |
| `read`, `thread` | `conversations.history/replies` | `*:history` |
| `send`(DM) | `conversations.open`, `chat.postMessage` | `im:write mpim:write chat:write` |
| `mark` | `conversations.mark` | `channels:write groups:write im:write mpim:write` |
| `edit`, `delete`, `send --at` | `chat.update/delete/scheduleMessage` | `chat:write` |
| `search` | `search.messages` | `search:read` |
| `react` | `reactions.add/remove` | `reactions:write` |
| `upload`, `download` | `files.getUploadURLExternal/completeUploadExternal`, `files.info` | `files:write`, `files:read` |
| `users`, `whois` | `users.list/info` | `users:read` |
| `status`, `presence` | `users.profile.set`, `users.setPresence` | `users.profile:write users:write` |
| `watch` | Socket Mode | `connections:write` + user 이벤트 |

**토큰 입력**: 인자로 받지 않는다(셸 히스토리와 `ps`에 남는다). 프롬프트(에코 없음), stdin, 환경 변수 `GSLACK_TOKEN` 중 하나로 받는다.

**토큰 저장**: macOS에서는 Keychain(`security` CLI, 추가 의존성 없음)에 저장한다. `security add-generic-password`에 `-w <값>` 인자를 쓰지 않고 stdin으로 넘긴다. 다른 OS에서는 `~/.config/gslack/credentials.json`(0600)에 저장한다. 설정 파일에는 토큰을 절대 평문으로 두지 않는다.

## 4. 기능 요구사항(MVP)

메시지는 `#channel`, `@user`(DM), 채널 ID, **메시지 permalink**(스레드나 메시지를 가리킬 때) 중 어느 형식으로 지정해도 된다. permalink 파서는 enterprise/Slack Connect 도메인도 받는다.

### 4.1 인증·프로필
```
gslack auth login [--profile acme] [--type user|session]   # 토큰/쿠키는 프롬프트·stdin·GSLACK_TOKEN으로
gslack auth status | list | use <profile> | logout [<profile>]
gslack --profile <name> <cmd>      # 명령 한 번에만 적용
```
- B의 쿠키는 raw `Cookie` 헤더 형식으로 받는다(`d=…; d-s=…`). Enterprise Grid는 `d-s`도 필요할 수 있다.

### 4.2 메시지·스레드·DM
```
gslack channels [--unread] [--type public,private,im,mpim]   # users.conversations(가입한 대화만)
gslack read <target> [-n 30] [--since 2h] [--before <ts>] [--mark]
gslack thread <permalink | target ts>
gslack send <target> [text | -]  [--thread <ts|permalink>] [--at <RFC3339|+2h>] [--tz Asia/Seoul]
gslack edit <permalink> <text>   · gslack delete <permalink> [--yes]
gslack unread                    # 안 읽은 채널/DM 요약(A는 근사치)
gslack mark <target> [--yes]     # 읽음 처리
gslack users [query] · gslack whois <@user>
```
- **읽음 정책**: `read`는 기본으로 읽음 처리하지 않는다(조회 명령은 상태를 바꾸지 않는다). `--mark`로 켠다. *(기본값, 이견 있으면 변경)*
- `read`/`thread` 출력에는 ts를 항상 표시한다(다음 명령의 입력이 되므로).
- `send --at`: 자연어 파싱은 하지 않는다. RFC3339나 `+2h` 같은 상대 시간만 받는다. 시간대는 `--tz`, 없으면 로컬. Slack 한계: 최대 120일 뒤, 채널당 5분에 30개.
- `edit/delete/mark`는 확인을 묻고, `--yes`로 생략한다.
- 캐시에 없는 사용자는 `users.info`로 폴백한다.

### 4.3 검색
```
gslack search <query> [-n 20] [--in #ch] [--from @user]   # search.messages, user token 필요
```
- `search.messages`는 legacy API라서 GUI 검색 결과와 다를 수 있다고 문서화한다.

### 4.4 리액션·파일·상태
```
gslack react <permalink> <emoji> [--remove]
gslack upload <file|-> <target> [--thread …] [--title …]   # files.getUploadURLExternal 흐름
gslack download <permalink|file-id> [-o dir]
gslack status set "<text>" [--emoji :x:] [--until 1h] · gslack status clear
gslack presence away|auto
```
- `download`: `file-id → files.info → url_private_download` 순서로 받는다. Bearer/쿠키는 **Slack HTTPS 호스트에만** 붙이고, remote/external 파일 URL에는 자격 증명을 보내지 않는다(slack-go v0.30.1 보안 변경과 같은 정책). 같은 이름이 있으면 덮어쓰지 않고 접미사를 붙이며, 임시 파일에 받은 뒤 rename해서 부분 다운로드를 남기지 않는다.

### 4.5 실시간·알림
```
gslack watch [targets…] [--notify] [--mentions-only]
```
- 새 메시지를 한 줄씩 stdout으로 스트리밍한다. `--json`이면 NDJSON으로 출력한다.
- Socket Mode envelope는 3초 안에 ack하고, `event_id`로 중복을 제거한다. 자기 메시지는 거른다.
- `--mentions-only`는 `<@나>`, `<!here>`, `<!channel>`, 내가 속한 `<!subteam^…>`을 포함한다. *(기본값)*
- 내가 참여한 스레드의 답글도 알린다. `message_changed/deleted`는 기본으로 거른다. *(기본값)*
- `--notify`면 멘션/DM이 올 때 macOS 알림을 띄운다(`osascript -e 'display notification …'`, 추가 의존성 없음).
- 연결이 끊기면 지수 백오프로 재연결한다.

## 5. 비기능 요구사항
- **출력**: 기본은 사람이 읽기 좋은 형식(사용자 이름 해석, 상대 시간, mrkdwn → 터미널 텍스트)이다.
- **`--json` 계약**: `internal/core`에 정규화 모델(Message/Channel/User)을 두고, 모든 명령과 `watch`가 같은 타입을 출력한다(TUI도 재사용). Slack 원본이 필요하면 `--raw`. 목록은 JSON 배열, `watch`는 NDJSON(한 줄에 Message 하나). 오류는 stderr에 `{"error": "<slack 오류 코드>", "message": "…"}`로 출력하고 종료 코드 규칙을 따른다.
- **mrkdwn 렌더링 체크리스트**: `<@U…>`, `<#C…|name>`, `<!here>`/`<!channel>`, `<!subteam^…>`, 링크, HTML 엔티티(`&amp;` 등), blocks만 있는 봇 메시지, 파일 첨부, `reply_count`, `subtype`.
- **캐시**: 사용자/채널 이름 ↔ ID 매핑을 `~/.cache/gslack/<profile>/`에 JSON으로 저장하고 TTL을 24시간으로 둔다. `--refresh`로 갱신한다.
- **레이트리밋**: `429 Retry-After`를 따른다(slack-go `OptionRetry`). 그 외에 profile+method 단위 limiter를 두고, 모든 호출은 context 취소를 따른다.
- **성능**: 캐시가 있을 때 `read` 명령은 1초 안에 끝나야 한다.
- **종료 코드**: 성공 0, 사용법 오류 2, API/인증 오류 1.

## 6. 기술 결정
- **언어/SDK**: Go 1.26, `github.com/slack-go/slack` v0.30.1(Web API, `socketmode`, RTM, `UploadFile`(external upload V2) 모두 포함).
- **xoxc 쿠키**: slack-go에는 쿠키 옵션이 없다. Web API 요청에는 `OptionHTTPClient`에 `Cookie` 헤더를 붙이는 RoundTripper를 넣고(약 10줄), RTM에는 `RTMOptionDialer`에 쿠키 Jar(Domain `.slack.com`)를 설정한 Dialer를 넣는다. 업로드 2단계는 다른 호스트로 POST하므로 RoundTripper가 거기서도 맞게 동작하는지 M0에서 확인한다(R7).
- **CLI 프레임워크**: `cobra`. 서브커맨드가 20개를 넘고, 채널/사용자 이름 셸 자동완성이 CLI 사용성의 핵심이라서 고른다.
- **브라우저 자동 로그인(slackdump `RodAuth`)**: 쓰지 않는다. 의존성이 무겁다(Chromium). 수동 입력으로 시작하고, 필요하면 나중에 데스크톱 앱 로컬 저장소에서 가져오는 기능을 검토한다.

## 7. 리스크·열린 질문
| # | 리스크 | 대응 |
|---|---|---|
| R1 | acme 워크스페이스에 내부 앱 설치 승인이 날지 불확실 | 완화: 사용자 계정으로 만든 내부 앱 4개가 이미 Acme에 있고, 그중 하나(Acme Works)는 xoxp가 발급된 상태다(2026-10-12 확인). 새 앱 설치 때 승인 요청이 뜨는지는 0.1에서 확인한다. 막히면 B로 보완한다 |
| R2 | xoxp로는 정확한 안 읽은 수를 구할 수 없다(`unread_count`는 DM만, 채널은 `last_read`가 안 올 수 있음) | M0 결과(2026-10-12): `last_read`는 모든 대화 유형에 온다. `unread_count`는 믿을 수 없다(0으로 옴). A는 `last_read` 이후 history 개수로 센다 — 정확하지만 대화당 API 2회라 느리다. B의 `client.counts`는 한 번에 끝난다 |
| R3 | Socket Mode로 *user* 이벤트(DM, 멘션)가 실제로 오는지 | Spike에서 검증한다 |
| R4 | xoxc + RTM 연결(쿠키 Dialer)이 동작하는지 | Spike에서 검증한다(M0을 막지 않음) |
| R5 | 세션 토큰이 로그아웃이나 보안 정책으로 만료됨 | `invalid_auth`를 받으면 재로그인 방법을 안내한다 |
| R6 | 비공식 API(`client.counts`)가 바뀔 수 있음 | 해당 기능만 실패하게 격리하고, 나머지는 계속 동작하게 한다 |
| R7 | xoxc 업로드 2단계(다른 호스트)에서 쿠키 RoundTripper가 동작하는지 | M0 0.3에서 업로드 1건으로 검증한다 |
| R8 | Socket Mode(A)만 연결하면 계속 away로 보일 수 있음 | M0 0.4에서 `users.getPresence`로 확인한다 |
| R9 | acme에서 xoxc 사용이 회사 보안 정책에 맞는지 | 사용 전에 확인한다 |

## 8. 마일스톤
- **M0 Spike**: A로 auth.test, 채널 목록, 버스트 테스트, Socket Mode 실시간을 확인한다(R2, R3, R8 해소). **A가 M0 게이트다.** B(xoxc, RTM, `client.counts`, R4·R7)는 독립 spike로 병행하고 M0을 막지 않는다.
- **M1 핵심(얇은 수직 슬라이스)**: 정규화 모델 → auth/profile → channels → read → send/DM → thread → unread/mark, users. 화려한 렌더링과 자동완성은 뒤로 미룬다.
- **M2**: search, react, upload/download, status/presence, edit/delete, `send --at`
- **M3**: watch + 알림, 렌더링 고도화, 셸 자동완성, 배포(goreleaser/Homebrew tap)
- **M4(별도)**: TUI
