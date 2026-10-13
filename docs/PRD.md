# gslack — PRD

> Slack GUI 없이 터미널에서 Slack을 쓰기 위한 Go CLI.
> 작성일: 2026-10-12 · 상태: M0~M3c 결과 반영(2026-10-13)

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

**MVP 이후 백로그**: 채널 join/leave, 핀, 북마크, DND/스누즈, 리마인더, Canvas/Lists, `watch --all-profiles`(여러 워크스페이스 동시 감시).

## 2. 사용자

본인과 터미널 중심으로 일하는 개발자. 여러 워크스페이스(acme 포함)에 속해 있다.

## 3. 인증 — 두 방식 모두 지원

| | A. 공식 앱(User token) | B. 브라우저 세션(**비공식**) |
|---|---|---|
| 자격 증명 | `xoxp-` (+ 실시간용 `xapp-`) | `xoxc-` + `d` 쿠키(`xoxd-`) |
| 준비 | 내부 앱을 만들고 워크스페이스에 설치(Acme는 관리자 승인 없이 설치됨, 0.1) | `gslack auth import` — Slack 데스크톱 앱 로컬 저장소에서 가져온다(macOS, c.1). 수동 입력(`auth login`에 xoxc + 쿠키)도 가능 |
| 권한 | 앱 manifest의 user scope 범위 | GUI 사용자와 동일 |
| 저장 | macOS Keychain | 0600 파일 — 값이 길어 Keychain에 안 들어간다(아래 토큰 저장) |
| 실시간 | Socket Mode(user 이벤트). 가입하지 않은 공개 채널 이벤트도 온다 — 직접 `<@나>`만 통과, 나머지는 가입 대화로 거른다. 재연결은 slack-go가 자동(0.4) | RTM(`rtm.connect` + 쿠키 Jar 다이얼러). 앱·xapp 없이 동작(0.5). 가입한 모든 대화의 메시지가 오고 `channel_type`이 없어 가입 대화 캐시로 판별(c.3) |
| 안 읽은 수 | 근사치: `last_read` 이후 history 한 페이지(100건, 넘으면 `100+`)를 센다. 대화당 2회 호출이라 느리다(DM 29초, `--all` 약 4분). GUI와 다를 수 있다(허들 알림 DM) | `client.counts`(0.4초)의 `has_unreads`로 대상을 줄인 뒤 A와 같이 센다. 최근 활동 대화만 와서 없는 대화는 읽음으로 본다. 실패하면 A로 폴백(c.2) |
| 프레즌스 | Socket Mode는 active로 만들지 않는다(0.4) | RTM 연결 중 active 관찰(0.5) — 데스크톱 앱과 겹친 관찰이라 단독 효과는 미확인 |
| 안정성 | 공식, 장기적으로 안정 | 비공식, ToS 회색지대, 로그아웃하면 만료, 무보장. Acme 사용은 사용자 결정(2026-10-13, R9) |

**워크스페이스별 비용**: A는 워크스페이스마다 앱을 따로 설치해야 하고, B는 워크스페이스별 `xoxc` + 공통 `d` 쿠키만 있으면 된다.

**우선순위**: A가 출시 기준이다(M0 게이트). B는 "비공식·실험적"으로 표기한다. B spike(2026-10-13) 통과 후 B에서만 빠른 `unread`(client.counts)와 앱 없는 `watch`(RTM)를 쓴다. 그 밖의 명령은 두 방식이 같은 코드 경로다.

**레이트리밋 확인 결과**: 2025-05-29부터 `conversations.history/replies`는 1회/분, 15건으로 축소됐지만, 이 제한은 *Marketplace 밖에서 상업 배포되는 앱*에만 적용된다. 공식 문서는 "Internal customer-built applications are not impacted"라고 명시한다. 따라서 직접 만든 내부 앱(A)은 Tier 3(50+/분, 보장치 아님)을 유지한다. → A를 기본 읽기 경로로 써도 된다. M0 0.2 버스트 테스트(limit=200 16회/7.5초)에서 429 없음 — 확인.
- **규칙**: 앱 설정에서 **Distribute App(public distribution)을 절대 켜지 않는다**. 켜면 internal 판정이 깨질 수 있다.

**필요한 user scope(A)**: `channels:history groups:history im:history mpim:history channels:read groups:read im:read mpim:read channels:write groups:write im:write mpim:write chat:write reactions:write files:read files:write search:read users:read usergroups:read users.profile:write`
**app-level scope**(`xapp`): `connections:write`. **user 이벤트**: `message.im message.mpim message.channels message.groups`(`app_mention`은 봇 이벤트라 없음 — 멘션은 `<@U…>` 매칭).
→ `slack-app-manifest.yaml`. 2026-10-13 코드에서 쓰지 않는 `reactions:read emoji:read users:write`(`presence` 명령을 뺌)를 manifest에서 뺐다. 이미 설치된 앱은 그대로 둬도 된다(scope가 넓을 뿐).

### 명령별 인증·scope 매트릭스(2026-10-13)
**공통**: 대상·이름 해석과 캐시 때문에 모든 대화 명령이 `users.conversations`·`users.list/info`(`*:read users:read`)를 쓴다 — 표에는 그 밖의 scope만 적는다. B는 scope가 없다(§3 권한). "B 라이브"는 session 프로필로 실제 호출해 본 것.

| 명령 | 주요 메서드 | A scope(공통 외) | B 라이브 |
|---|---|---|---|
| `auth login/import/status` | `auth.test` | — | ✓(0.3, c.1) |
| `channels`, 자동완성 | `users.conversations` | — | ✓(0.3) |
| `read`, `thread` | `conversations.history/replies` | `*:history` | `read` ✓(c.1) |
| `unread` | A: `conversations.info`+`history` · B: `client.counts` 후 같은 방식 | `*:history` | ✓(c.2) |
| `mark`, `read --mark` | `conversations.info/history/mark` | `*:history *:write` | 미확인 |
| `send`, `send --at` | `chat.postMessage/scheduleMessage`, 새 DM이면 `conversations.open` | `chat:write`(+`im:write mpim:write`) | 미확인 |
| `edit`, `delete` | 원문 확인 `conversations.replies` → `chat.update/delete` | `*:history chat:write` | 미확인 |
| `search` | `search.messages` | `search:read` | 미확인 |
| `react` | `reactions.add/remove` | `reactions:write` | 미확인 |
| `upload` | `files.getUploadURLExternal/completeUploadExternal`(새 DM이면 `conversations.open`) | `files:write`(+`im:write`) | ✓(0.3) |
| `download` | `files.info` + 파일 URL(permalink면 `conversations.replies`) | `files:read`(+`*:history`) | ✓(0.3) |
| `users`, `whois` | `users.list/info`, `users.getPresence`, `bots.info` | — | 이름 해석 ✓(c.1) |
| `status`, `status set/clear` | `users.profile.set`, `users.info` | `users.profile:write` | 미확인 |
| 그룹 멘션(`watch`, 렌더링) | `usergroups.list` | `usergroups:read`(없으면 모든 그룹 멘션 통과) | 미확인 |
| `watch` | A: Socket Mode · B: RTM | A: `connections:write`(xapp) + user 이벤트(`*:history`) | 연결 확인 중(c.3) |

**토큰 입력**: 인자로 받지 않는다(셸 히스토리와 `ps`에 남는다). 프롬프트(에코 없음), stdin, 환경 변수 `GSLACK_TOKEN` 중 하나로 받는다.

**토큰 저장**: macOS에서는 Keychain(`security` CLI, 추가 의존성 없음)에 저장한다. `security add-generic-password`에 `-w <값>` 인자를 쓰지 않고 stdin으로 넘긴다(tty를 떼야 stdin을 읽는다). `security`의 getpass가 128B에서 자르므로 직렬화한 값이 120B를 넘으면(사실상 session의 xoxc+쿠키) — 그리고 다른 OS는 — `~/.config/gslack/credentials.json`(0600)에 저장한다(macOS session의 at-rest는 FileVault에 맡긴다, 사용자 결정 c.1). 설정 파일(`config.json`)에는 토큰을 두지 않는다.

## 4. 기능 요구사항(MVP)

메시지는 `#channel`, `@user`(DM), 채널 ID, **메시지 permalink**(스레드나 메시지를 가리킬 때) 중 어느 형식으로 지정해도 된다. permalink 파서는 enterprise/Slack Connect 도메인도 받는다.

### 4.1 인증·프로필
```
gslack auth login [--profile acme]   # 토큰/쿠키는 프롬프트·stdin·GSLACK_TOKEN으로. 종류는 접두사(xoxp→user, xoxc→session)
gslack auth import [team]          # B: Slack 데스크톱 앱에서 xoxc+d 가져오기(macOS, 비공식)
gslack auth status | list | use <profile> | logout [<profile>]
gslack --profile <name> <cmd>      # 명령 한 번에만 적용
```
- B의 쿠키는 raw `Cookie` 헤더 형식으로 받는다(`d=…; d-s=…`). Enterprise Grid는 `d-s`도 필요할 수 있다.

### 4.2 메시지·스레드·DM
```
gslack channels [--type public,private,im,mpim]   # users.conversations(가입한 대화만)
gslack read <target> [-n 30] [--since 2h] [--before <ts>] [--mark]
gslack thread <permalink | target ts>
gslack send <target|permalink> [text | -]  [--thread <ts>] [--at <RFC3339|2h>]
gslack edit <permalink> <text>   · gslack delete <permalink> [--yes]
gslack unread [--type …] [--all] # 안 읽은 대화 요약(근사치, 기본 DM·그룹 DM)
gslack mark <target> [--yes]     # 읽음 처리
gslack users [query] · gslack whois <@user>
```
- **읽음 정책**: `read`는 기본으로 읽음 처리하지 않는다(조회 명령은 상태를 바꾸지 않는다). `--mark`로 켠다. *(기본값, 이견 있으면 변경)*
- `read`/`thread` 출력에는 ts를 항상 표시한다(다음 명령의 입력이 되므로).
- `send --at`: 자연어 파싱은 하지 않는다. RFC3339(오프셋 포함)나 `2h` 같은 상대 시간만 받는다. 미래인지만 검사하고 Slack 한계(120일, 채널당 5분 30개)는 Slack 오류 그대로. 예약 목록·취소는 Slack 앱에서.
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
gslack upload <target|permalink> <file> [--thread …] [--title …] [--comment …]   # files.getUploadURLExternal 흐름, stdin 없음
gslack download <permalink|file-id> [-o dir]
gslack status set "<text>" [--emoji :x:] [--until 1h] · gslack status clear
```
- `presence`는 뺐다(M2 2.4): 수동 away는 실제 접속 상태를 보장하지 않고, 조회는 `whois`로 한다.
- `download`: `file-id → files.info → url_private_download` 순서로 받는다. Bearer/쿠키는 **Slack HTTPS 호스트에만** 붙이고, remote/external 파일 URL에는 자격 증명을 보내지 않는다(slack-go v0.30.1 보안 변경과 같은 정책). 같은 이름이 있으면 덮어쓰지 않고 접미사를 붙이며, 임시 파일에 받은 뒤 rename해서 부분 다운로드를 남기지 않는다.

### 4.5 실시간·알림
```
gslack watch [targets…] [--all] [--notify]
```
- 새 메시지를 한 줄씩 stdout으로 스트리밍한다. `--json`이면 NDJSON으로 출력한다.
- A는 Socket Mode(envelope 3초 안 ack, `event_id` 중복 제거), B는 RTM(xapp 불필요). 같은 필터·출력. 자기 메시지는 거른다.
- 기본은 DM·그룹 DM과 `<@나>`, `<!here>`, `<!channel>`, 내가 속한 `<!subteam^…>`(`usergroups:read`) 멘션만. 대상을 주면 그 대화 전체, `--all`이면 가입한 모든 대화.
- 내가 참여한 스레드의 답글도 알린다. `message_changed/deleted`는 기본으로 거른다. *(기본값)*
- `--notify`면 멘션/DM이 올 때 macOS 알림을 띄운다(`osascript -e 'display notification …'`, 추가 의존성 없음).
- 연결이 끊기면 slack-go가 지수 백오프로 재연결한다. 인증 실패와 Slack의 거절(`ok:false`)은 재시도하지 않고 종료한다.

## 5. 비기능 요구사항
- **출력**: 기본은 사람이 읽기 좋은 형식(사용자 이름 해석, 상대 시간, mrkdwn → 터미널 텍스트)이다.
- **`--json` 계약**: `internal/core`에 정규화 모델(Message/Channel/User)을 두고, 모든 명령과 `watch`가 같은 타입을 출력한다(TUI도 재사용). Slack 원본이 필요하면 `--raw`. 목록은 JSON 배열, `watch`는 NDJSON(한 줄에 Message 하나). 오류는 stderr에 `{"error": "<slack 오류 코드>", "message": "…"}`로 출력하고 종료 코드 규칙을 따른다.
- **mrkdwn 렌더링 체크리스트**: `<@U…>`, `<#C…|name>`, `<!here>`/`<!channel>`, `<!subteam^…>`, 링크, HTML 엔티티(`&amp;` 등), blocks만 있는 봇 메시지, 파일 첨부, `reply_count`, `subtype`.
- **캐시**: 사용자/채널 이름 ↔ ID 매핑을 `~/.cache/gslack/<profile>/`에 JSON으로 저장하고 TTL을 24시간으로 둔다. `--refresh`로 갱신한다.
- **레이트리밋**: `429 Retry-After`를 따른다(slack-go `OptionRetryConfig`, Retry-After 없으면 5초). profile+method limiter는 만들지 않았다(M1b — 0.2에서 429 없음, `unread`만 직접 대기). 모든 호출은 context 취소를 따른다.
- **성능**: 캐시가 있을 때 `read` 명령은 1초 안에 끝나야 한다.
- **종료 코드**: 성공 0, 사용법 오류 2, API/인증 오류 1.

## 6. 기술 결정
- **언어/SDK**: Go 1.26, `github.com/slack-go/slack` v0.30.1(Web API, `socketmode`, RTM, `UploadFile`(external upload V2) 모두 포함).
- **xoxc 쿠키**: slack-go에는 쿠키 옵션이 없다. Web API 요청에는 `OptionHTTPClient`에 `Cookie` 헤더를 붙이는 RoundTripper를 넣고(약 10줄), RTM에는 `RTMOptionDialer`에 쿠키 Jar(Domain `.slack.com`)를 설정한 Dialer를 넣는다. 쿠키는 https `*.slack.com`으로만 붙인다. 업로드 2단계(다른 호스트)에서도 동작 확인(R7).
- **CLI 프레임워크**: `cobra`. 서브커맨드가 20개를 넘고, 채널/사용자 이름 셸 자동완성이 CLI 사용성의 핵심이라서 고른다.
- **브라우저 자동 로그인(slackdump `RodAuth`)**: 쓰지 않는다. 의존성이 무겁다(Chromium). 대신 데스크톱 앱 로컬 저장소에서 가져온다(`auth import`: LevelDB는 goleveldb, 쿠키는 시스템 `sqlite3` + Keychain "Slack Safe Storage" + stdlib pbkdf2).

## 7. 리스크·열린 질문
| # | 리스크 | 상태(2026-10-13) |
|---|---|---|
| R1 | acme에 내부 앱 설치 승인이 날지 | **해소** — 관리자 승인 없이 설치됨(0.1) |
| R2 | xoxp로 정확한 안 읽은 수 | **해소**(근사치로 수용) — `last_read`는 모든 유형에 오고 `unread_count`는 0이라 안 쓴다. §3 안 읽은 수 |
| R3 | Socket Mode로 user 이벤트(DM, 멘션)가 오는지 | **해소** — DM·채널 멘션·그룹 멘션 수신, 자기 메시지 필터, 자동 재연결(0.4) |
| R4 | xoxc + RTM(쿠키 Dialer) | **해소** — 연결·수신 확인(0.5), `watch`에 구현(c.3). RTM 스레드 답글의 `parent_user_id` 라이브 확인 중 |
| R5 | 세션 토큰 만료 | **대응** — `invalid_auth` 등에 재로그인 안내(session은 `auth import`) |
| R6 | 비공식 API 변경 | **대응** — `client.counts` 실패는 A로 폴백(c.2). RTM 거절은 오류로 종료(c.3). `auth import`는 Slack 업데이트로 깨질 수 있다고 help에 명시 |
| R7 | xoxc 업로드 2단계 쿠키 | **해소** — 업로드·다운로드 확인(0.3) |
| R8 | Socket Mode만 연결하면 away | **해소** — 그렇다(§3 프레즌스). active가 필요하면 B의 RTM |
| R9 | acme에서 xoxc 사용이 보안 정책에 맞는지 | **사용자 결정**(2026-10-13) — 진행 |

## 8. 마일스톤
- **M0 Spike**: A로 auth.test, 채널 목록, 버스트 테스트, Socket Mode 실시간을 확인한다(R2, R3, R8). **A가 M0 게이트다.** B(xoxc, RTM, `client.counts`, R4·R7)는 독립 spike로 병행하고 M0을 막지 않는다.
- **M1 핵심(얇은 수직 슬라이스)**: 정규화 모델 → auth/profile → channels → read → send/DM → thread → unread/mark, users. 화려한 렌더링과 자동완성은 뒤로 미룬다.
- **M2**: search, react, upload/download, status, edit/delete, `send --at`(presence는 뺌)
- **M3**: watch + 알림, 렌더링 고도화, 셸 자동완성, 배포(goreleaser — Homebrew tap은 안 만듦, 사용자 결정)
- **M3c**: B 활용 — `auth import`, `unread`(client.counts), `watch`(RTM), 이 PRD 정리
- **M4(별도)**: TUI
