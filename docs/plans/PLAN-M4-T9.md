# M4 T9 구현 계획 — 본 데까지 읽음

> 2026-10-15 · 대상: TASKS M4 T9 · 상태: Codex·Opus 리뷰 반영(Fable은 한도로 Opus가 대신)
> 목표(사용자 요청): "어떤 채널은 쭉 아래까지 봤으면 다 읽은 것으로, 어떤 채널은 하나하나 봐야" — 사용자 결정: **Esc로 목록에 돌아갈 때 j로 지나간 마지막 메시지까지 mark**. 끝까지 보면 전부, 중간이면 나머지는 안 읽음. `m`은 그대로 전부.
> PRD-TUI의 "여는 것만으로는 읽음 아님"은 그대로 — 아래 §4대로 j/k를 눌러야 센다.

## 1. 본 데까지

- `m.seen string`: **history 미리보기**에서 고른 메시지 ts의 최댓값. 미리보기를 받을 때(`showPreview` — 처음 고른 것 = 안 본 첫 메시지, 모르면 마지막) 그 ts로, `selectMsg`마다 `max`. 스레드 미리보기(줄 자체가 스레드)는 안 센다 — 스레드 읽음은 비공개 API. T8로 연 스레드 안의 이동도 안 센다(채널 `seen`은 `back`에 든 채널 미리보기 때 값 그대로).
- `m.pvRead string`: 미리보기의 `last_read`(previewMsg.read). `seen <= pvRead`면 할 일 없음.

## 2. Esc로 목록에 갈 때

- 채널 미리보기에서 Esc(포커스 끔, `back` 없음)면 `seen > pvRead`일 때 `markRead(ch, seen)` Cmd → 기존 `markedMsg{ch, seen}` → `setDone`(줄의 latest ≤ seen이면 치움 — 끝까지 봤을 때). 중간이면 줄은 남고 **count를 남은 수로**: 미리보기 메시지 중 `seen`보다 뒤인 것의 수(20개 넘게 밀렸으면 `more` 그대로 — 아래 한계).
- 미리보기가 실패했거나 받는 중이면(`pvMsgs` 없음) 아무것도.
- 실패는 기존 "치우기 실패: …" 상태줄.
- `q`로 끝낼 때는 mark하지 않는다(나가는 길에 API를 기다리지 않게) — *ponytail: 끝낼 때도 원하면*.

## 3. 다른 경로와

- `m`: 지금 그대로 전부(`pv.ts`까지). `m`은 포커스를 끄므로 Esc 경로를 타지 않는다.
- 답장 뒤 읽음: 지금 그대로(채널은 답한 메시지·미리보기 최신 중 늦은 것까지).
- 실시간으로 그 대화에 새 메시지가 오면 줄의 latest가 seen보다 뒤라 `setDone`이 치우지 않는다(지금 규칙).

## 4. 리뷰 반영(Codex·Opus) — 위보다 이것이 우선

- **j/k를 눌러야 센다**(Opus 6): 처음 고른 메시지는 안 센다 — Enter→Esc는 아무것도 안 바꾼다(사용자 결정 "j로 지나간 데까지", PRD 그대로). j/k를 누르면 **움직인 뒤 고른 메시지**를 센다 — 안 읽은 게 하나뿐이면 j(더 못 가도)로 그것까지.
- **상태는 `m.pv`에 합친다**(Opus 10): `pv{ch, ts, read, seen}` — history 미리보기 응답(`msg.latest != ""`)에서 함께 바꾸고(seen = read), `selectMsg`는 `!pvRow.thread() && pv.ch == pvRow.ch.ID`일 때만 max(Opus 4·5, Codex 6 — T8로 연 스레드는 가짜 줄이 thread()라 안 센다, 스레드 줄 미리보기가 앞 채널을 다시 mark하지 않는다).
- **읽음 위치를 모르면 끈다**(Opus 1, Codex 5): `read == ""`(conversations.info 실패)면 안 한다.
- **20개 넘게 밀렸으면 끈다**(Opus 2): 미리보기 첫 메시지가 이미 read 뒤면 그 앞에 못 본 안 읽은 메시지가 있을 수 있다 → seen까지 mark하면 그것들이 읽음이 된다. 안 하고 상태줄 "앞에 안 본 것이 더 있다 — m으로 치우기".
- **줄은 끝까지 봤을 때만 치우고 count는 안 고친다**(Codex 2·3·4·7, Opus 3·7·11): `seen >= pv.ts`(채널 최신)일 때만 `setDone(ch, seen)`, 중간이면 줄·count·more 그대로(다음 R이 바로잡음). forMe 채널 줄(latest = 멘션)이 멘션까지만 보고 사라지지 않는다. mark를 낸 뒤 `pv.read = seen`(같은 요청을 또 안 낸다).
- `Esc`에 seq++는 안 한다(Codex 1): 받는 중인 미리보기는 목록 포커스에서도 보이는 게 맞다(지금 동작).

## 테스트

- `TestTUISeenMark`(previewMsg 직접): read …01, 메시지 …01 …02 …03 …04(처음 고른 것 …02). Enter → Esc → mark 없음. Enter → j(…03) → Esc → mark …03, 줄 남음. Enter → j → j(…04) → k → Esc → mark …04(max), 줄 done.
- read "" → j → Esc → 없음. 첫 메시지가 read 뒤(20개 넘음) → 없음 + 상태줄. 스레드 줄 미리보기 → j → Esc → 앞 채널 mark 없음. T8: 채널 j → 스레드 열기 → 스레드 안 j → Esc → Esc → 채널에서 간 데까지. forMe 채널 줄(latest = 멘션)에서 멘션까지만 → 줄 남음.

## 안 하는 것

- 화면에 보였는지(스크롤)로 세기 — 고른 메시지로만. 20개 넘게 밀린 대화의 정확한 남은 수(다음 R이 바로잡는다). 스레드 읽음.
