# M4 T8 구현 계획 — 미리보기에서 스레드 열기

> 2026-10-15 · 대상: TASKS M4 T8 · 상태: Codex·Opus 리뷰 반영(Fable은 한도로 Opus가 대신)
> 목표(사용자 요청 "아래쪽 판넬에서 쓰레드를 보여주는 기능"): 사용자 결정 — **고른 메시지에서 Enter**로 아래 판넬이 그 스레드로, Esc로 채널에 돌아감, r은 스레드 안에.

## 1. 열기

- 미리보기 포커스, **history 미리보기**(스레드 미리보기가 아님)에서 고른 메시지의 `ReplyCount > 0`이면 `Enter` → 그 스레드 미리보기. 답글이 없으면 지금처럼 무시.
- 스레드 줄은 새 타입 없이 `pvRow`의 복사본: `latest = 고른 메시지`, `latest.ThreadTS = 고른 ts`, `latest.TS = ""`(→ `thread()`가 참 — `replyThread`·`preview`·머리 "— 스레드"가 지금 규칙 그대로 그 스레드로). `TS = ""`는 `setDone`이 아무 줄도 안 치우게 한다(스레드 답장 뒤 `sentMsg`의 ts가 ""). 주석으로 남긴다.
- 열기 전 채널 미리보기를 모델에 둔다: `m.back *pvSaved{row, raw, msgs, head, sel, off}`(YOffset). 받는 동안은 지금 Enter처럼 "불러오는 중…".

## 2. 돌아가기

- `Esc`: `m.back`이 있으면 채널로 — 저장한 것을 되돌려 `renderPreview`(그사이 폭이 바뀌었을 수 있다) → `highlight` → `SetYOffset(off)`, `m.back = nil`, `m.seq++`(받는 중인 스레드 응답을 버린다). 네트워크 없음. 없으면 지금처럼 목록으로.
- 목록에서 다른 줄 `Enter`·`m`으로 목록에 갈 때 `m.back = nil`.

## 3. 스레드 안의 키

- `r`: 그 스레드(지금 스레드 미리보기 규칙 — 고른 것과 상관없이). 보낸 뒤 다시 미리보기는 `pvRow`(그 스레드), `back`은 그대로라 Esc로 채널에.
- `o`: 고른 답글의 permalink(지금 그대로).
- `m`: 대화를 치우는 키라 **`back.row`(채널 줄)** 기준 — 스레드 줄로 하면 TUI 안에서만 치우는 스레드 규칙을 타 아무것도 안 된다. `m.pv`(history 최신)는 채널 미리보기 때 둔 그대로라 본 데까지 치운다.
- 키 힌트: history 미리보기에서 고른 메시지에 답글이 있으면 `Enter 스레드`, 열린 스레드면 `Esc 채널`.

## 4. 리뷰 반영(Codex·Opus) — 위보다 이것이 우선

- **열기 조건은 `sel.ThreadTS != ""`, 스레드 ts는 `sel.ThreadTS`**(Opus 1): history의 ThreadTS는 답글 달린 부모(= TS)나 thread_broadcast(= 부모 ts)뿐(convert가 답글 없는 것은 지운다). broadcast는 ReplyCount가 0이라 `ReplyCount > 0`이면 놓친다. 조건은 `canOpen()` 하나로 Enter와 키 힌트가 같이 쓴다.
- **`TS = ""` 가짜 줄은 `pvRow` 전용**(Codex 2·3, Opus 2): `m.rows`에 절대 넣지 않는다 — 정렬·`apply`·`merge`·`setDone`이 `latest.TS`를 본다. 지금 경로(`send`의 스레드 분기 → `setDone(ch, "")`은 아무것도 안 치움)에선 안전하다고 둘 다 확인. 주석을 만드는 곳에 남긴다. *ponytail: 명시 필드(`threadTS`)는 가짜 줄이 목록에 닿는 경로가 생기면*.
- **`m`은 `target()` 한 줄**(Opus 3): `k == "m" && m.back != nil`이면 `m.back.row`. 치우는 범위는 지금처럼 `m.pv.ts`(채널 미리보기를 받을 때의 최신 — 스레드를 연 동안 온 새 메시지는 안 치운다, Codex 5. 지금 규칙과 같다).
- **복원은 `showSel()`로**(Opus 4·9): YOffset 저장을 빼고 `{row, raw, msgs, head, sel}`만 — 폭·높이가 바뀌었으면 옛 offset이 틀린다. 복원 = 되돌림 → `renderPreview` → `highlight` → `showSel`.
- **`back = nil`은 포커스가 꺼지는 두 곳(Esc·`m`)만**(Codex 4, Opus 8): 불변식 "back != nil ⇔ 포커스 중이고 연 스레드". 목록 Enter의 nil은 중복이라 뺀다. 저장은 `preview()` 전에(그게 `pvRow`를 덮는다, Opus 5).
- **스레드 안 답장은 채널을 mark하지 않는다**(Opus 6): 채널 미리보기에서 고른 메시지에 `r`하면 채널을 본 데까지 치우지만, 스레드를 열고 답하면 스레드 규칙(안 치움). 채널 읽음은 T9("본 데까지 읽음" — Esc로 목록에 갈 때)가 맡는다. 계획에 남긴다.
- 늦은 `sentMsg`·`markedMsg`(Codex 1·6): 보내는 중엔 입력줄이 키를 다 받아 Esc가 안 되고, `markedMsg`는 ch·ts로 줄만 치워 화면을 안 바꾼다 — 세대 토큰은 안 넣는다.
- 키 힌트: `Esc 채널`(back 있음), `Enter 스레드`(`canOpen()`).

## 테스트

- `TestTUIOpenThread`: 채널 history 미리보기(답글 달린 메시지 하나) → 그것 골라 Enter → `conversations.replies` 요청(ts = 그 메시지), 구분선 "— 스레드". `r` → `thread_ts` = 그 ts. Esc → 채널 미리보기, 고른 메시지 그대로·화면 안(폭을 바꾼 뒤에도), 요청 없음. 다시 Esc → 목록.
- broadcast(ReplyCount 0, ThreadTS = 부모) 골라 Enter → replies가 부모 ts로. 답글 없는 메시지 Enter → 아무것도(seq 그대로). 스레드 미리보기(줄 자체가 스레드)에서 Enter → 아무것도.
- 스레드 연 채로 `m` → 채널 mark(ts = history 최신). 스레드 받는 중 Esc → 늦게 온 스레드 응답이 채널 미리보기를 덮지 않음.

## 안 하는 것

- 스레드 안의 스레드(없음), 스레드에서 더 깊은 뒤로가기 스택(한 단계면 된다), 스레드 읽음(비공개 API — 지금처럼).
