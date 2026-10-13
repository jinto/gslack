# M1b 구현 계획 — 쓰기 슬라이스(send, thread, unread, mark)

> 2026-10-12 · 대상: TASKS 1.10~1.12, `read --mark` · 상태: 초안(리뷰 전)

## 범위

**포함**: `thread`, `send`(DM은 `conversations.open`), `unread`, `mark`, `read --mark`.

**제외**: `send --at`·`edit`·`delete`(M2 2.5), 실시간(M3), B 경로(`client.counts`, R9 대기).
**profile+method limiter는 만들지 않는다**(TASKS 1.5에서 넘어온 항목 폐기). 근거: 아래 사전 측정에서 `conversations.info` 264회를 동시 8개로 보내도 12.8초, 429는 3회였고 이미 있는 `OptionRetryConfig`가 처리한다. 팬아웃은 `unread` 하나뿐이라 그 안의 동시성 상한(세마포어)으로 충분하다.

**안전**: 회사(Acme) Slack에 쓰는 라이브 테스트(`send`, `mark`, `conversations.open`)는 매번 사용자 허락 후, 본인 DM(self-DM)에만 한다. 자동 테스트는 전부 httptest 가짜 서버.

## 사전 측정(2026-10-12, Acme, 읽기만)

- `client.counts`/`users.counts`는 xoxp에서 `not_allowed_token_type` — A 경로에선 못 쓴다(예상대로).
- `users.conversations`의 `updated`는 메시지 활동을 반영하지 않는다(오늘 메시지가 온 DM도 10/8 값) — 후보 거르기에 못 쓴다.
- `search.messages after:<날짜>`는 가입 안 한 공개 채널까지 섞여 2일에 4,116건 — 못 쓴다.
- `conversations.info`:
  - **im**: `last_read`, `latest`(메시지 객체), `unread_count`, `unread_count_display`가 온다. 오늘 DM은 `unread_count: 1`로 정확했다(M0의 "전부 0"은 당시 안 읽은 DM이 없었기 때문).
  - **채널/비공개/mpim**: `last_read`만 온다. `latest`·`unread_count` 없음.
- 처리량: info 264회 / 동시 8 / 12.8초 / 429 3회.

## 구조

새 파일 2개, 기존 파일 수정.

```
internal/cli/
  write.go    # send, mark (쓰기 명령은 한 파일에)
  unread.go   # unread
  read.go     # + thread, read --mark
  session.go  # + dmID(open bool)
```

`core`/`store`는 바꾸지 않는다.

## 핵심 결정

### 1.11 `thread <permalink | target ts>`
- 인자: permalink 하나, 또는 target + ts. permalink가 답글 링크면 `ThreadTS`를 루트로 쓴다.
- `conversations.replies`(cursor 페이지네이션, 전부). 출력은 `read`의 `printMessages` 재사용 — 첫 줄이 부모, 나머지가 답글. `--json`이면 `[]core.Message`, `--raw`면 원본 배열(이 경우 `--raw` 허용 명령에 `thread` 추가).
- `read`가 답글 permalink를 받으면 지금은 "M1b" 안내로 거절 — 안내 문구를 `gslack thread <링크>`로 바꾼다.

### 1.10 `send <target> [text | -] [--thread <ts|permalink>]`
- 본문: 인자(여러 개면 공백으로 이음), `-` 또는 인자 없음 + stdin이 TTY 아님 → stdin 전체. 앞뒤 공백 제거 후 비면 usage 오류. 4만 자 초과 usage 오류(Slack 한도).
- 본문은 mrkdwn 그대로 보낸다(`<@U…>` 멘션을 쓸 수 있게). 변환하지 않는다.
- **대상 해석은 정확 일치만**: `@이름`이 부분 일치(1.14)로만 찾아지면 보내지 않고 usage 오류 + 찾은 사람 표시("정확한 이름이나 ID로"). 잘못된 사람에게 보내는 사고 방지. ID·permalink·`#채널`(정확 일치)은 그대로.
- DM: `channelID`가 im을 못 찾으면 `conversations.open(users=U)`로 연다. `session.dmID(ctx, userID, open bool)`로 분리 — `read`는 open=false(읽기 명령은 상태를 안 바꾼다), `send`만 true.
- `--thread`: ts 또는 permalink. permalink면 채널이 target과 같은지 확인, target을 생략하고 `send --thread <permalink> text`도 허용? → **아니다**. target은 항상 필수(인자 규칙 단순하게). permalink의 채널이 다르면 usage 오류. 답글 permalink면 그 `ThreadTS`를 쓴다.
- `chat.postMessage`(`OptionText(text, false)`, `OptionTS`). 재시도: slack-go RetryConfig는 429만 재시도하므로 중복 전송 위험은 429 응답 뒤 재시도뿐 — 429는 처리 안 된 요청이라 안전.
- 출력: 보낸 메시지의 `ts`와 permalink 한 줄. `--json`이면 `core.Message`(channel, ts, thread_ts, text, permalink).
- 확인 프롬프트 없음(PRD §4: 확인은 edit/delete/mark). 단 대상이 `#channel`이고 멤버 수가 큰 경우 등 경고는 YAGNI.

### 1.12a `mark <target> [ts] [--yes]`
- ts 생략 시 최신 메시지 ts(`history limit=1`). 그 ts까지 읽음 처리(`conversations.mark`).
- 확인: TTY면 "#general을 <ts>(HH:MM, 미리보기 40자)까지 읽음 처리할까요? [y/N]". 비TTY이면서 `--yes` 없으면 usage 오류(스크립트가 실수로 멈추지 않게).
- `read --mark`: 출력한 메시지 중 가장 최신 ts로 mark. `--before`나 permalink(과거 구간)와 함께면 usage 오류 — 과거를 읽고 읽음 위치를 뒤로 옮기는 사고 방지. `read --mark`는 명시적 플래그라 확인을 묻지 않는다.

### 1.12b `unread [--type …] [-n 3]`
- 대상: 가입한 대화 전체(`loadChannels`), `--type`으로 좁힘.
- 대화마다 `conversations.info`. 동시성 8(세마포어, `errgroup` 없이 `sync.WaitGroup` + 채널). 하나라도 실패하면 첫 오류로 실패(부분 결과는 버림) — 단 `channel_not_found`/`not_in_channel`은 건너뛴다(캐시가 오래된 경우).
- 판정:
  - im: `latest.ts > last_read`이면 안 읽음, 개수는 `unread_count_display`.
  - 그 외: `last_read`가 있으면 `conversations.history(oldest=last_read, limit=100)`, 본인 메시지 제외 개수. `has_more`면 "100+". `last_read`가 `0000000000.000000`(한 번도 안 읽음)이면 history limit=100 동일.
  - 2차 호출(history)도 같은 세마포어.
- 출력: 개수 내림차순 정렬은 아니고 **최신 메시지 시각 내림차순**. 한 줄: `#name  N  HH:MM @who: 미리보기`. `-n`은 대화별 미리보기 메시지 수(기본 1, 0이면 개수만). `--json`이면 `[{channel: core.Channel, count, has_more, messages: []core.Message}]`.
- 진행 표시: stderr가 TTY면 `확인 중 123/264` 한 줄 갱신. 비TTY면 없음.
- 정확도 주의(PRD R2): A는 근사치. 스레드 답글·뮤트 채널 구분 없음 — 출력 첫 줄에 쓰지 않고 `--help`에만 적는다.

## 테스트 계획

가짜 서버(`fakeSlack`)에 `conversations.replies/open/mark/info`, `chat.postMessage`를 추가하고 요청 파라미터를 검증한다.

- thread: permalink(부모·답글) → replies 파라미터, 페이지네이션, 출력 순서.
- send: 인자/stdin 본문, 빈 본문 오류, `--thread` ts·permalink(채널 불일치 오류), DM 없음 → open 호출 후 post, 부분 일치 이름 → 거절(post 호출 0회).
- read: im 없음 → open을 **호출하지 않음**.
- mark: `--yes` 없이 비TTY → usage(호출 0회), `--yes` → 최신 ts로 mark. `read --mark` + `--before` → usage.
- unread: im(latest>last_read), 채널(history oldest=last_read, 본인 메시지 제외), 0건 제외, 동시성 상한(가짜 서버에서 동시 진입 수 최댓값 ≤ 8).
- 라이브(Acme): 읽기(`thread`, `unread`)는 자유. `send @jaekwon`(self-DM) 1건, `mark` self-DM 1건은 **사용자 허락 후**.

## 열린 질문(리뷰어에게)

1. `send`의 부분 일치 거절 — 너무 엄격한가? 대안: TTY면 "@minsu(민수)에게 보낼까요?" 확인.
2. `unread`가 대화 264개에 info 264회 + history ~100회 — 기본 범위를 줄여야 하나(예: im/mpim/private만, `--all`로 전체)?
3. `read --mark`에 확인을 안 묻는 것 — 플래그가 명시적이니 충분한가?
4. 이 계획에서 더 빼도 되는 것(ponytail 관점)은?

## R. 리뷰 반영(Codex·Fable, 2026-10-12) — 본문과 충돌하면 이 절이 우선

**send**
- permalink를 target으로 주면 **그 스레드에 답글**(답글 링크면 `ThreadTS`, 아니면 `TS`). 채널 최상위 오발송 방지(Fable Blocker, Codex Major). `--thread`는 ts만(`tsRe`).
- 본문은 인자 하나 또는 `-`/stdin(`RangeArgs(1,2)`). 여러 인자 잇기 삭제 — `send #ch @x hi` 같은 순서 실수가 조용히 전송되는 것 방지. 4만 자 사전 검사 삭제(Slack `msg_too_long`이 그대로 나온다).
- 부분 일치 거절: `findUser` 결과를 `send`에서 EqualFold 재확인. 오류에 붙여넣을 수 있는 정확한 `@handle`.
- DM: `dmID` 대신 `channelID`가 `errNoDM`(usageError로 감싼 센티널)을 돌려주고, `send`만 그때 `conversations.open`. `read`는 그대로 open 안 함.
- `PostMessageWithResponseContext`로 받은 `slack.Message`를 `s.message()` → `printMessages`/`printJSON`. `read`와 같은 모양, 스레드 답글이 최상위로 간 경우도 응답 `thread_ts`로 드러난다. 사람용 출력 앞에 `→ #general`/`→ @minsu` 한 줄(stderr).
- 재시도: slack-go는 429만 재시도한다. Slack은 429 요청을 처리하지 않으므로 유지. 타임아웃 등 네트워크 오류는 재시도하지 않는다(사용자가 다시 보내기 전 확인 필요 — `--help`에 한 줄).

**mark / read --mark**
- `mark <target|permalink> [--yes]` — ts 위치 인자 삭제, 특정 위치는 permalink로. ts 생략 시 `history limit=1`, 비면 오류.
- 공통 `markTo(ch, ts)`: `conversations.info`의 `last_read`보다 과거면 **거절**(읽음 위치를 되돌리지 않는다, Codex Blocker). 같으면 no-op.
- 확인은 `confirm()` — 프롬프트는 stderr, 답은 `a.in` 한 줄(에코 있음). 비TTY에서 `--yes` 없으면 usage, 호출 0회.
- `read --mark`: raw 분기보다 먼저, 결과 0건이면 mark 안 함. `--before`/permalink와 함께면 usage. 확인 없음.

**unread**
- 기본 전체, `--type`으로 좁힘(`--type im,mpim`이 빠른 경로 — `--help`에). `-n` 삭제, 미리보기는 최신 1건.
- 판정 하나로 통일: im은 `latest.ts > last_read`(nil 가드)일 때만 history로 넘어가고, 나머지는 바로 history(`oldest=last_read, limit=100`). 셀 때 본인 메시지와 `channel_join`/`channel_leave` 제외. `has_more`면 `N+`.
- 동시 8, `context.WithCancel` — 첫 오류에서 나머지 취소. `channel_not_found`/`not_in_channel`은 건너뜀.
- 정렬은 최신 메시지 시각 내림차순. `--json`은 cli 안 인라인 struct `{channel, count, more, latest}`.
- 진행 표시는 stderr가 TTY일 때만, 끝나면 지움.

**thread**: `Limit: 200`, `thread <target> <ts>`의 ts는 `tsRe` 검증, `--raw` 허용 목록은 `slices.Contains([]string{"read","thread"}, …)`.

**제외 유지**: limiter, `send --yes`, `unread --all`.

## R2. 구현 중 측정으로 바뀐 것(2026-10-12)

- **unread 기본 범위를 im,mpim으로 뒤집었다**(Q2는 Codex 답으로). 사전 측정(info만 264회 12.8초)과 달리 실제 전체 스캔은 info 264 + history ~190 ≈ 450회로 3분 27초~4분, 기본 재시도(3회)로는 429에서 실패했다. DM만(184개) 29초. `--all`로 전체, `--all`과 `--type`은 함께 못 쓴다.
- 429: 전역 `MaxRetries`는 올리지 않는다(send 재시도 특성과 "429가 계속되면 유한 시간에 실패" 테스트 유지). `unread` 안에서만 `waitRateLimit`이 Retry-After를 기다려 다시 부른다(ctx 취소 존중).
- 한 번도 안 읽은 대화의 `last_read`는 `0000000000.000000`이고 history `oldest`로 주면 `invalid_ts_oldest` — oldest를 비운다.
- mpim의 `conversations.info`에는 `latest`가 없다(im만 있음) — mpim은 history가 필요하다.
