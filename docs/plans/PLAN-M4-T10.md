# M4 T10 구현 계획 — 나중에 보기

> 2026-10-15 · 대상: TASKS M4 T10 · 상태: Codex·Opus 리뷰 반영(Fable은 한도로 Opus가 대신)
> 목표(사용자 요청 "어떤 메시지는 나중에 보기에 넣어두고 싶다"): 사용자 결정 — **Slack 자체 "나중에(Later)"**(앱·폰에서도 보이게).
> spike(2026-10-15, session 프로필): `saved.list` ok(20개, `item_type=message`·`item_id=채널`·`ts`·`state=in_progress`), `saved.add`(`item_type=message, item_id=채널, ts`) ok → 목록 맨 위에 생김. `stars.*`는 옛 "저장" — 안 쓴다.

## 1. 키

- `l`(later): 대상 메시지를 나중에 넣는다. 대상은 `o`와 같은 규칙 — 미리보기 포커스면 고른 메시지, 목록이면 줄의 latest(`target("o")`를 그대로). 스레드 답글이면 그 답글 ts(Slack도 답글 단위로 넣는다).
- 성공 → 상태줄 "나중에 넣었다". 실패 → "나중에 실패: …". 읽음·줄은 건드리지 않는다(나중에 넣는 것과 치우는 것은 따로 — 치우려면 `m`).
- 키 힌트에 `l 나중에`.

## 2. API

- `saved.add`는 비공식이라 `client.counts`·`users.prefs.get`처럼 `private()`로(팀 URL, isSlackURL, ok:false는 오류). `private`에 폼 값 인자를 하나 더(`url.Values`, 기존 둘은 nil).
- **session 프로필만**(spike: xoxp로는 안 해 봄 — `client.counts`·`users.prefs.get`이 xoxp에서 거절된 것과 같은 계열로 본다). user 프로필에서 `l` → 상태줄 "나중에는 session 프로필만(gslack auth import)", 요청 없음. *ponytail: user에서 되는지는 사용자가 원하면 spike*.
- Cmd 하나(`laterMsg{err}`), 늦게 와도 상태줄만이라 seq 없음.

## 3. 리뷰 반영(Codex·Opus) — 위보다 이것이 우선

- **대상은 `target("o")`**(Codex 1): 포커스면 고른 메시지(스레드 미리보기도 고른 답글), 목록이면 줄의 latest. 미리보기를 받기 전이면 줄의 latest(아직 아무것도 안 봤으니 그 줄 — 계획에 남긴다, Opus 5).
- **ts가 비면 요청 없음**(Opus 1, Codex 2): T8로 연 스레드를 받는 중·실패면 가짜 줄의 `latest.TS = ""`가 대상이 된다.
- **`private(ctx, method, v, kv ...string)`**(Opus 2, Codex 3·4): 가변 인자라 기존 호출은 그대로. 폼은 token을 먼저 만들고 kv를 더한다(호출자는 상수 키만). 응답은 작은 구조체로 받는다(`v` nil 없음).
- **프로필 검사 없음**(Opus 7): `l`은 사용자가 직접 누르는 키라 user 프로필(xoxp)이 거절하면 그 이유("나중에 실패: …")를 그대로 보인다. 되면 그대로 된다 — §2의 "session만"·안내 문구는 뺀다.
- 늦은 응답의 상태줄(Codex 7): `m`·`o`와 같이 둔다(지금 규칙).
- 테스트용 `apiURL`이 isSlackURL을 건너뛰는 것(Codex 5)은 기존 `private()` 그대로 — 이번 범위 밖.
- 키 힌트: 목록·포커스·연 스레드·canOpen 네 곳에 `l 나중에`(상태가 길면 잘린다 — 지금 규칙).
- **spike**(Opus 3·4, 2026-10-15 사용자 실행): 이미 있는 것을 다시 넣으면 ok·목록에 하나(앱에서 뺐다 다시 넣은 것도 ok — 앱의 "나중에"와 같은 목록). 스레드 답글 ts는 확인 중 — 안 되면 상태줄에 Slack의 거절 이유가 나온다.

## 테스트

- `TestTUILater`(fakeSlack에 `saved.add`): 미리보기에서 j로 고른 메시지 `l` → 요청 `item_type=message`·`item_id=C…`·`ts=고른 것`, 상태 "나중에 넣었다", 줄 done 아님·mark 없음. 목록에서 `l` → 줄의 latest. ok:false → "나중에 실패: …". 연 스레드 받는 중 `l` → 요청 없음. 폼에 token이 실린다.

## 안 하는 것

- 나중에 목록 보기·빼기·완료 표시(요청 없음 — 앱에서), CLI 명령(`gslack later`), 알림 시각(reminder).
