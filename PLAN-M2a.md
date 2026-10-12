# M2a 구현 계획 — search, react, edit/delete, status/presence

> 2026-10-12 · 대상: TASKS 2.1, 2.2, 2.4, 2.5(edit/delete만) · 상태: 초안(리뷰 전)
> M2b로 미룸: 2.3 upload/download, 2.5 `send --at`

## 범위

| 명령 | API | scope(이미 manifest에 있음) |
|---|---|---|
| `search <query> [-n 20] [--in <대상>] [--from @user]` | `search.messages` | `search:read` |
| `react <메시지> <emoji> [--remove]` | `reactions.add/remove` | `reactions:write` |
| `edit <메시지> <본문 \| -> [--yes]` | `chat.update` | `chat:write` |
| `delete <메시지> [--yes]` | `chat.delete` | `chat:write` |
| `status [set <text> [--emoji :x:] [--until 1h] \| clear]` | `users.info`, `users.profile.set` | `users:read`, `users.profile:write` |
| `presence [away \| auto]` | `users.getPresence`, `users.setPresence` | `users:read`, `users:write` |

`<메시지>` = permalink 하나, 또는 `<대상> <ts>` 두 인자(`thread`와 같은 규칙).

**안전**: react/edit/delete/status/presence는 회사 Slack에 쓴다. 라이브 테스트는 사용자 허락 후, 본인 DM의 테스트 메시지(이미 있는 "gslack 테스트")에만. status/presence는 동료에게 보이므로 따로 허락. 자동 테스트는 httptest 가짜 서버만.

## 구조

```
internal/cli/
  message.go  # msgRef(args) 공통 파서 + fetchMessage(ch, ts, threadTS) — thread/react/edit/delete
  search.go   # search
  write.go    # + react, edit, delete
  me.go       # status, presence
```

`core`/`store`는 바꾸지 않는다(검색 결과도 `core.Message`).

## 핵심 결정

### 공통: 메시지 참조
- `msgRef(args) (target core.Target, ts, threadTS string, rest []string, err)`:
  - 첫 인자가 permalink면 ts·thread_ts는 링크에서, 나머지 인자는 `rest`.
  - 아니면 둘째 인자가 ts(`tsRe`)여야 한다. `threadTS`는 모름.
- `thread`도 이걸 쓰도록 바꾼다(지금 인라인 switch).
- `fetchMessage(ctx, ch, ts, threadTS)`: edit/delete 확인용 원문.
  - threadTS가 있으면 `conversations.replies(ts=threadTS, oldest=ts, inclusive, limit=1)`.
  - 없으면 `conversations.history(latest=ts, inclusive, limit=1)`, 받은 메시지의 ts가 다르면 "못 찾았다 — 스레드 답글이면 permalink로".

### 2.1 search
- 쿼리 조립: 사용자 쿼리 + `--in`·`--from`을 Slack 검색 문법으로.
  - `--in #ch` → `in:#<name>`(채널 이름 해석 후), `--in @user` → `in:<@U…>`, ID면 `in:<#C…>`.
  - `--from @user` → `from:<@U…>`(`findUser`, 부분 일치 허용 — 읽기 명령).
  - 라이브로 각 문법이 실제 결과를 거르는지 확인(읽기 API).
- `sort=timestamp desc`, `count=min(n,100)`, n>100이면 page를 넘긴다.
- 결과 순서는 **최신 먼저**(검색은 최근 것을 찾는 용도). `read`와 다르다는 점을 `--help`에.
- 출력: 사람용 `MM/DD HH:MM  #채널  @이름: 미리보기` + 다음 줄에 permalink(다음 명령의 입력). `--json`은 `[]core.Message`(permalink는 API가 준 값).
- `--help`에 "search.messages는 legacy API — Slack 앱 검색과 결과가 다를 수 있다".

### 2.2 react
- `react <메시지> <emoji> [--remove]`. emoji는 `:thumbsup:`/`thumbsup` 모두 받아 콜론 제거.
- `already_reacted`(추가)·`no_reaction`(제거)은 성공으로 본다(멱등) — 사람용 출력에 "이미 …".
- 확인 없음(되돌리기 쉬움). 출력: `:emoji: → <permalink>`. `--json`: `{channel, ts, emoji, removed}`.

### 2.5 edit / delete
- 둘 다 `fetchMessage`로 원문을 받아:
  - 본인 메시지가 아니면 usage 오류(Slack도 `cant_update_message`를 주지만 확인 프롬프트 전에 거른다).
  - 확인(stderr): edit은 "원문 → 새 본문" 미리보기, delete는 원문 미리보기. 비TTY에서 `--yes` 없으면 usage, API 호출 0회(`mark`와 같은 규칙).
- edit 본문은 `send`와 같은 `body()`(인자 하나 또는 `-`/stdin).
- edit 출력: 수정된 메시지(`show`/`--json`은 `core.Message`). delete 출력: `삭제했다 <permalink>` / `{channel, ts}`.

### 2.4 status / presence
- `status`(인자 없음): 내 `users.info`에서 status_text/emoji/expiration을 보여준다.
- `status set <text> [--emoji :x:] [--until 1h|RFC3339]`: `users.profile.set`. `--until`은 `parseSince`와 반대 방향(미래)이라 `parseUntil` 새로 — 같은 문법(Nd, Go duration, RFC3339).
- `status clear`: 빈 text·emoji·expiration 0.
- `presence`(인자 없음): 내 presence. `presence away|auto`: `users.setPresence`.
- 확인 없음(되돌리기 쉬움).

## 테스트 계획
가짜 서버에 `search.messages`, `reactions.add/remove`, `chat.update/delete`, `users.profile.set`, `users.setPresence` 추가.
- msgRef: permalink/대상+ts/잘못된 ts.
- search: `--in #general` → `in:#general`, `--from @jaekwon` → `from:<@U1…>`, n>100 페이지, JSON permalink.
- react: 콜론 제거, `already_reacted` → exit 0, `--remove` → reactions.remove.
- edit/delete: 남의 메시지 → exit 2·호출 0, 비TTY+--yes 없음 → exit 2·호출 0, `--yes` → update/delete 파라미터, 답글 permalink → replies로 원문 조회.
- status: `--until 2h` → expiration ≈ now+2h, clear → 빈 값. presence: away/auto 외 → usage.
- 라이브: search 읽기 자유. react/edit/delete는 self-DM 테스트 메시지에 허락 후. status/presence는 허락 후(또는 생략).

## 열린 질문(리뷰어에게)
1. search 결과 최신 먼저 vs `read`처럼 오래된 것 먼저?
2. `already_reacted`를 성공으로 보는 것 — 스크립트 관점에서 맞나?
3. edit/delete 본인 메시지 사전 확인 — 관리자 권한으로 남의 메시지 삭제 가능한 경우를 막아버리는데 괜찮나?
4. `status`/`presence`를 하위 명령 없는 조회로 겸하는 것 — `status get`이 더 명확한가?
5. 더 뺄 것(ponytail)?

## R. 리뷰 반영(Codex·Fable, 2026-10-12) — 본문과 충돌하면 이 절이 우선

**라이브 확인(읽기)**: search 수식어 `in:#name`, `in:<#C…>`, `in:<#D…>`, `in:<@U…>`, `from:<@U…>`, `from:@handle` 모두 같은 결과로 걸러진다(Codex의 "in:<#C…> 불가" 지적은 실측과 다름 → ID 형태로 통일: 해석 오류 없음). `conversations.replies(ts=<답글 ts>)`는 그 답글 하나를 돌려준다 → 원문 조회는 경로 하나.

- **msgRef**: `msgRef(args) (core.Target, rest []string, error)` — `<대상> <ts>`면 `t.TS=args[1]`. permalink의 `thread_ts`도 ts 형식 검증(core.ParseTarget). 명령마다 `len(rest)` 검증. `thread`도 사용.
- **fetchMessage**: `replies(ts, oldest=ts, latest=ts, inclusive, limit=1)` + `Timestamp==ts`인 것만. history 분기·threadTS 인자 삭제.
- **edit/delete 순서**: 인자 검증 → `!yes && !TTY`면 usage(**HTTP 요청 0회**) → open → fetch → **본인 메시지만**(Codex: user token의 chat.update/delete는 본인 것만, 봇 메시지는 거절. Fable의 "검사 삭제"는 기각 — 삭제는 되돌릴 수 없어 이중 방어) → TTY면 미리보기 확인 → 실행.
- **edit 본문은 인자 하나만**(stdin 없음 — stdin이면 확인 프롬프트를 못 띄운다). 출력은 `수정했다 <permalink>` / `{channel, ts, text}`(chat.update는 메시지 전체를 안 돌려준다, 재조회 안 함). help: "text만 바꾸고 blocks는 지워진다".
- **search**: `-n`은 1~100(페이지 루프 삭제, Tier 2). 받은 결과는 `read`처럼 오래된 것 위·최신 아래로 뒤집는다(Fable — 터미널 끝에 최신). 사람용 한 줄 `ts  MM/DD HH:MM  #채널  @이름: 미리보기` — ts가 앞에 있어 `react #채널 <ts>`로 이어진다. `--json`은 `[]core.Message`(permalink는 API 값, thread_ts는 permalink에서). 변환은 `searchFrom`.
- **react**: `AddReactionContext(ctx, emoji, slack.NewRefToMessage(ch, ts))`. 정확히 `already_reacted`/`no_reaction`만 성공. `--json {channel, ts, emoji, reacted(최종 상태), changed}`.
- **status**: 범용 `SetUserProfile` 금지(Codex Blocker — 다른 프로필 필드를 덮어쓴다). `SetUserCustomStatusContext`/`UnsetUserCustomStatusContext`만. 조회는 캐시 말고 `GetUserInfoContext`. `--emoji` 생략 시 Slack 기본(💬) — help에. `--until`은 `parseSince`를 `parseWhen(flag, s, now, future)`로 일반화해서 공유. `--json {status_text, status_emoji, status_expiration}`.
- **presence는 뺀다**(Codex Cut — 수동 away는 실제 접속 상태를 보장하지 않고 R8 검증도 남음, 조회는 `whois`가 이미 한다).
- 파일: `search.go`, `me.go`(status) 둘만 새로. msgRef/fetchMessage는 `read.go`, react/edit/delete는 `write.go`.
