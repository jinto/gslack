# M1a 구현 계획 — 읽기 전용 수직 슬라이스

> 2026-10-12 · 대상: TASKS 1.1~1.9, 1.13 · 상태: Codex·Fable 리뷰 반영(아래 §R이 본문보다 우선)

## 범위

**포함**: 루트/전역 플래그, 정규화 모델, 자격 증명 저장소, `auth login/status/list/use/logout`, 클라이언트 팩토리, 이름 캐시, target 파서, 기본 포맷터, `channels`, `users`, `whois`, `read`.

**제외(M1b로)**: `send`, `thread`, `unread`, `mark`, `read --mark`. 이 슬라이스는 Slack에 **쓰는 API를 하나도 호출하지 않는다**(라이브 테스트도 읽기만). profile+method limiter도 M1b의 `unread` 팬아웃과 함께 넣는다 — M1a는 명령당 호출 수가 적어 `OptionRetry`로 충분하다.

## 구조

```
main.go                     # cli.Execute() → os.Exit(code)
internal/core/              # 의존성 없는 순수 로직
  model.go                  # Message, Channel, User (+ slack 타입 → core 변환)
  target.go                 # ParseTarget: #ch, @user, C/G/D ID, permalink
  mrkdwn.go                 # Render(text, names) → 터미널 텍스트
internal/store/             # 디스크에 닿는 것
  config.go                 # ~/.config/gslack/config.json (프로필 메타데이터만)
  secret.go                 # Keychain(macOS) / 0600 파일(그 외)
  cache.go                  # ~/.cache/gslack/<profile>/{users,channels}.json, TTL 24h
internal/cli/               # cobra 명령 + 클라이언트 팩토리 + 출력
  root.go auth.go channels.go users.go read.go client.go output.go
```

패키지 3개. 인터페이스는 `secret` 백엔드 하나만(Keychain/파일 — 실제 구현 2개, 테스트에서 파일 백엔드 사용).

## 핵심 결정

### 1.1 루트·플래그·종료 코드
- 모듈 경로 `github.com/jinto/gslack`, Go 1.26, 의존성: `slack-go/slack v0.30.1`, `spf13/cobra`, `golang.org/x/term`(비밀번호 프롬프트).
- 전역 플래그: `--profile`, `--json`, `--raw`, `--refresh`. 환경 변수 `GSLACK_PROFILE`.
- 종료 코드: 0 성공, 2 사용법 오류(cobra 인자/플래그 오류, target 파싱 실패), 1 API/인증/IO 오류.
- 오류 출력: 기본은 `gslack: <message>` 한 줄. `--json`이면 stderr에 `{"error":"<code>","message":"…"}`. `<code>`는 Slack 오류 코드(`channel_not_found`, `invalid_auth` 등) 또는 내부 코드(`usage`, `not_logged_in`).
- `invalid_auth`/`token_revoked`면 메시지에 `gslack auth login --profile <p>` 재로그인 안내를 붙인다(R5).

### 1.2 정규화 모델(`--json` 계약)
```go
type User    struct { ID, Name, RealName, DisplayName, TZ string; IsBot, Deleted bool }
type Channel struct { ID, Name, Kind string /* public|private|im|mpim */; User string /* im 상대 */; Topic string; IsArchived bool }
type Message struct {
    Channel, TS, ThreadTS, User, Username /* 해석된 이름 */, Text, Subtype string
    ReplyCount int; Edited bool; Files []File; Reactions []Reaction
}
type File     struct { ID, Name, Mimetype string; Size int }
type Reaction struct { Name string; Count int }
```
- JSON 필드는 snake_case, 빈 값은 `omitempty`(단 `ts`, `channel`, `id`는 항상).
- 목록 명령은 JSON 배열, 단건(`whois`)은 객체. `--raw`는 slack-go 원본 구조를 그대로 출력.
- `Text`는 Slack 원본 mrkdwn 그대로(렌더링은 사람용 출력에서만).

### 1.3 자격 증명 저장소
- 저장 단위: 프로필당 JSON 한 덩어리 `{"token":"xoxp-…","app_token":"xapp-…","cookie":"d=…"}`.
- macOS: `security -i`에 stdin으로 `add-generic-password -U -s gslack -a <profile> -w <quoted>` 명령을 흘려 넣는다 → 비밀값이 argv(`ps`)에 안 나온다. 읽기는 `security find-generic-password -s gslack -a <profile> -w`(출력은 stdout). 삭제는 `delete-generic-password`.
- 그 외 OS(또는 `GSLACK_SECRET_BACKEND=file`): `~/.config/gslack/credentials.json`, 0600, 디렉터리 0700, 원자적 쓰기(temp+rename).
- `config.json`에는 토큰을 절대 두지 않는다. 내용: `{"current":"acme","profiles":{"acme":{"kind":"user","team":"Acme","team_id":"T…","user_id":"U…","url":"https://acme.slack.com/"}}}`.

### 1.4 auth
- `gslack auth login [--profile p]`: 토큰 입력 우선순위 = `GSLACK_TOKEN` 환경 변수 → stdin이 TTY가 아니면 stdin 첫 줄 → TTY 프롬프트(에코 없음). `xoxc-`면 쿠키도 같은 방식으로(`GSLACK_COOKIE` → stdin 둘째 줄 → 프롬프트). 앱 토큰은 선택(`GSLACK_APP_TOKEN` 또는 프롬프트에서 빈 입력 허용).
- 저장 전에 `auth.test`로 검증하고, 응답의 team/user를 config에 기록. 프로필 이름 기본값은 팀 도메인(`acme`).
- `status`: 현재 프로필로 `auth.test` → 사용자/팀/종류 출력. `list`: config의 프로필 목록(현재 표시). `use <p>`: current 변경. `logout [p]`: secret 삭제 + config에서 제거 + 캐시 디렉터리 삭제.

### 1.5 클라이언트 팩토리
- `slack.New(token, OptionHTTPClient(hc), OptionRetry(3))`. `kind=session`이면 `hc.Transport`를 쿠키 RoundTripper로 감싼다: 요청 호스트가 `slack.com` 또는 `*.slack.com`일 때만 `Cookie` 헤더 추가.
- 모든 호출은 `cmd.Context()`(Ctrl-C 취소)를 쓰는 `…Context` 메서드로.

### 1.6 캐시 + target 파서
- 캐시 파일: `users.json`(`users.list` 전체), `channels.json`(`users.conversations`, 4종 타입, 아카이브 제외). `{fetched_at, items}` 형태, TTL 24h, `--refresh`면 무시하고 다시 받는다. 원자적 쓰기, 디렉터리 0700.
- 사용자 조회 miss → `users.info` 한 번 → 캐시에 추가 저장.
- `ParseTarget(s) → Target{Kind: channel|user|id|permalink, Name, ID, TS, ThreadTS}`
  - `#name` → 채널 이름, `@name` → 사용자 이름(DM), `C…/G…/D…`(대문자+숫자 9~11자) → ID
  - permalink: 호스트가 `slack.com` 또는 `*.slack.com`(enterprise `*.enterprise.slack.com` 포함), 경로 `/archives/<ID>/p<16자리>` → `ts = 앞10.뒤6`, 쿼리 `thread_ts` 있으면 ThreadTS
  - 그 외 → 사용법 오류
- 이름 → ID 해석은 cli에서 캐시로. `@user`의 DM 채널 ID는 channels 캐시의 `im` 항목(`User` 필드) 매칭 — 없으면 `conversations.open`이 필요하지만 그건 쓰기 scope 동작이라 M1a에서는 "DM 기록 없음" 오류로 끝낸다(M1b `send`에서 처리).

### 1.7 기본 포맷터(`core.Render`)
- `<@U123>`/`<@U123|name>` → `@이름`, `<#C123|name>` → `#name`(이름 없으면 캐시), `<!here>`/`<!channel>`/`<!everyone>` → `@here`…, `<!subteam^S…|@team>` → `@team`, `<url|label>` → `label (url)`, `<url>` → `url`, `&amp; &lt; &gt;` 디코드.
- 한 줄 형식: `<ts>  <HH:MM 또는 MM-DD HH:MM>  @user: text` + 스레드면 `[N replies]`, 파일이면 `[file: name]`. 여러 줄 텍스트는 들여쓰기. 상대 시간은 날짜 헤더(`── 오늘 ──`)로 대신(YAGNI: "3m ago" 형식은 넣지 않음).
- 색상 없음(M3 렌더링 고도화로).

### 1.8 `channels [--type public,private,im,mpim]`
- 캐시에서 출력. im은 상대 사용자 이름으로 표시. `--unread`는 M1b.

### 1.9 `read <target> [-n 30] [--since 2h] [--before <ts>]`
- `conversations.history`(limit=min(n,200), 커서 페이지네이션으로 n까지), `--since`는 Go duration(`2h`, `30m`) 또는 RFC3339 → `oldest`, `--before` → `latest`.
- 오래된 것부터 출력(터미널에서 아래가 최신). 읽음 처리하지 않는다.
- target이 permalink면 그 메시지 기준 `latest=ts inclusive`로 앞쪽 n개.

### 1.13 `users [query]`, `whois <@user|U…>`
- 캐시에서 이름/실명/표시명 부분 일치(대소문자 무시). `whois`는 `users.info` + `users.getPresence`(읽기).

## 테스트 계획
- 단위(`go test ./...`, 네트워크 없음):
  - `core`: ParseTarget 테이블 테스트(정상/enterprise/thread_ts/잘못된 입력), Render 테이블 테스트(멘션·채널·특수 멘션·서브팀·링크·엔티티), 모델 JSON 직렬화(필드명·omitempty) 골든.
  - `store`: 파일 secret 백엔드 0600·라운드트립, config 저장에 토큰 문자열이 없는지, 캐시 TTL 만료/`--refresh`.
  - `cli`: 쿠키 RoundTripper가 slack.com 호스트에만 헤더를 붙이는지(`httptest`), 종료 코드 매핑, 오류 JSON 형식.
- Keychain: macOS에서 테스트용 서비스 이름(`gslack-test`)으로 저장→읽기→삭제 1회, `ps`에 값이 안 나오는지는 구조상(argv에 없음)으로 확인.
- 라이브(읽기만, Acme): spike 토큰 파일을 stdin으로 `auth login` → `auth status` → `channels --type im` → `users jae` → `whois @jaekwon` → `read '#general' -n 5` / `--json` / `--raw` → permalink로 `read`. 성능: 캐시 있을 때 `read` < 1s 측정.
- `go vet ./...`, `gofmt -l` 깨끗.

## 열린 질문(리뷰어에게)
1. 패키지 3개 분할이 적절한가, 더 줄일 수 있나?
2. `security -i` stdin 방식이 argv 노출을 실제로 피하는가, 더 단순한 방법이 있나?
3. `@user` DM을 M1a에서 쓰기 없이 해석하는 방식(캐시의 im 매칭)이 충분한가?
4. 정규화 모델 필드 중 빠졌거나 과한 것은?

## R. 리뷰 반영(Codex·Fable, 2026-10-12) — 본문과 충돌하면 이 절이 우선

**자격 증명(§1.3)**
- `security -i` 방식을 버린다. `add-generic-password -U -s gslack -a <profile> -w`(`-w`를 마지막에, 값 없이) + stdin으로 값을 **두 줄 반복**해서 넘긴다. 실측 결과 `"` `\` `$` 백틱 `;` `%`가 섞인 값도 그대로 저장되고 그대로 읽혔고, 값은 argv에 나타나지 않는다.
- 한 줄만 넘기면 빈 값이 저장되는데도 종료 코드가 0이다(Fable 실측). 그래서 쓰기 직후 `find-generic-password -w`로 다시 읽어 바이트 단위로 비교하고, 다르면 오류(exit 1)로 끝낸다. 출력 끝의 `\n`은 잘라낸다.
- `find -w`는 값에 비ASCII 문자가 있으면 hex로 출력한다(실측). 그래서 저장값은 ASCII만 허용하고(토큰·쿠키는 ASCII), 아니면 거부한다. 키체인 경로 인자는 쓰지 않는다.
- Keychain 테스트는 opt-in 통합 테스트(`GSLACK_KEYCHAIN_TEST=1`, 서비스 `gslack-test`)로 분리한다.
- `GSLACK_TOKEN`(+`GSLACK_COOKIE`)이 설정돼 있으면 프로필·Keychain 없이 그 토큰을 바로 쓴다. 스크립트와 CI용이다.

**쿠키 RoundTripper(§1.5)**: `req.Clone(ctx)` 뒤에 `Header.Set("Cookie", raw)`로 붙인다. 호스트 판정은 `host=="slack.com" || HasSuffix(host, ".slack.com")`이다. 부정 테스트: `evilslack.com`, `slack.com.evil.com`.

**재시도·취소(§1.5)**: `OptionHTTPClient` 뒤에 `OptionRetryConfig(cfg)`를 둔다. cfg는 `DefaultRetryConfig()`에서 시작해 MaxRetries=3, RetryAfterDuration=5s로 바꾼다(Retry-After가 없을 때 기본 60s × 3 멈춤 방지). 429 전용이다. `main.go`는 `signal.NotifyContext` + `ExecuteContext`로 Ctrl-C를 전파한다.

**패키지 경계**: `core`는 자체 타입과 순수 함수만 둔다. slack-go → core 변환은 `cli/convert.go`에 둔다.

**오류·종료 코드(§1.1)**
- `SilenceUsage/SilenceErrors`, `usageError` 타입, `SetFlagErrorFunc`로 cobra 오류를 감싸서 exit 2를 판별한다.
- 코드 추출: `slack.SlackErrorResponse` → `.Err`, `*slack.RateLimitedError` → `rate_limited`, `slack.StatusCodeError` → `http_<n>`, `context.Canceled` → `canceled`, 그 외 → `internal`.
- `--json`과 `--raw`는 상호 배타이고, 같이 쓰면 사용법 오류다.

**캐시(§1.6)**: 이름에 의존하는 명령은 캐시가 없거나, 만료됐거나, `--refresh`면 먼저 읽기 API로 채운다(`GetUsersContext`, `GetConversationsForUserContext` 커서 루프). 손상된 JSON은 miss로 처리한다. 사용자 miss는 `users.info` 폴백 후 캐시에 추가한다(Slack Connect 외부 사용자 대응).

**target 파서(§1.6)**
- 대화 ID는 `^[CGD][A-Z0-9]{8,}$`, 사용자 ID는 `^[UW][A-Z0-9]{8,}$`(길이 상한 없음, Enterprise `W`). mrkdwn의 `<@W…>`도 처리한다.
- `@name`은 정확 handle(`name`) → `display_name` → `real_name` 순으로 EqualFold 매칭한다. 2건 이상 걸리면 후보를 나열하는 사용법 오류다.
- `#name`은 유니코드 이름을 그대로 받는다. **접두사 없는 단어는 채널 이름으로 시도한다**(bash에서 `#`이 주석이 되는 문제 대응).
- `@user` DM: channels 캐시의 im을 매칭하고, miss면 캐시를 한 번 새로 채운 뒤 다시 찾는다. 그래도 없으면 "DM 기록 없음" 오류다(`conversations.open`은 쓰기라 M1b). 라이브에서 `is_open=false` im도 오는지 확인한다.

**모델(§1.2)** — 본문 struct를 다음으로 교체(JSON 태그 snake_case):
- `Message{channel, ts, thread_ts?, user, user_name, bot?, text, subtype?, reply_count?, edited?, files?, reactions?, team?, permalink}`
  - `channel`: history 응답에는 없으므로 요청 채널 ID를 넣는다.
  - `user`: 봇이면 `bot_id`. `user_name`은 users 캐시 → `bot_profile.name` → `username` → ID 순으로 정한다.
  - `text`: 비어 있으면 `attachments[].fallback`과 blocks의 section·rich_text 평문을 추출해서 채운다(렌더링이 아니라 추출).
  - `permalink`: config의 team url + `/archives/<C>/p<ts에서 점 제거>`로 조립한다(API 호출 없음). 스레드 답글이면 `?thread_ts=`를 붙인다.
- `Channel{id, name, kind, user?, shared?}`: IsArchived는 항상 false라서 삭제하고, Topic도 뺀다.
- `User{id, name, real_name, display_name, title?, status_text?, status_emoji?, tz?, bot?, deleted?}`: Email은 scope가 없어서 넣지 않는다. Presence는 whois 출력에서만 합성한다.
- `File{id, name, mimetype}`, `Reaction{name, count}`
- `--raw`: read는 페이지를 합친 slack-go `[]Message` 배열을 출력한다.

**read(§1.9)**
- permalink면 `Latest=TS, Inclusive=true`로 읽는다. `--before`는 해당 ts를 제외(exclusive)한다.
- 스레드 답글 permalink(`thread_ts`가 있고 ts와 다름)면 "스레드 답글은 `gslack thread`(M1b)" 사용법 오류(exit 2)를 낸다.
- `--since`는 Go duration에 `d`(일) 접미사를 더 받고, RFC3339도 받는다.

**mrkdwn(§1.7)**: `<!date^…|fallback>` → fallback, `<mailto:x|y>` → y을 처리한다. `<…|…>`를 먼저 분리한 뒤 엔티티를 디코드한다.

**테스트 보강**
- 변환기 픽스처: 봇, blocks-only, 스레드 부모, 편집됨, 파일, channel 주입
- httptest: history 페이지네이션, oldest/latest/inclusive 파라미터, 429 재시도
- ParseTarget: `W…`, 12자 이상 ID, 유니코드, bare word, 답글 permalink
- 기타: 쿠키 RoundTripper 부정 케이스, 종료 코드 2, 캐시 cold-start와 손상 JSON
