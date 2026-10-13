# M4 T2 구현 계획 — `gslack tui` 처리(r/m/o)

> 2026-10-13 · 대상: TASKS M4 T2, PRD-TUI.md §3·§5 · 상태: Codex·Fable 리뷰 반영
> 목표: 보고→읽고→답하고→치우기 루프 완성. `r` 답장, `m` 치우기, 답장 뒤 자동 읽음, `o` 브라우저. 실시간은 T3.

## 흐름

### `r` 답장
1. 목록이 비어 있지 않으면 `m.reply = &tuiReply{row: 커서 줄(값 복사), thread: replyThread(row)}` + `textinput` 포커스. 줄을 값으로 잡아 두므로 `r` 전에 띄운 fill의 `filledMsg`가 입력 중 도착해 줄을 바꿔도 대상은 그대로. 결과(`sentMsg`·`markedMsg`)는 커서가 아니라 **대화 ID로** 줄을 찾고, 없으면 상태줄만.
2. **대상 규칙(`replyThread`)**: `row.thread()`(답글, `ThreadTS != TS` — 미리보기와 같은 술어. 답글 달린 부모는 스레드가 아니다)면 `ThreadTS`, 채널(public/private)이면 `latest.TS`(멘션에 채널 본문으로 답하지 않게), DM·그룹 DM이면 `""`(대화에 바로). T2의 줄은 DM·그룹 DM뿐이라 채널 갈래는 T3에서 쓰이지만 규칙은 한 줄이라 지금 넣는다.
3. 입력 중 키는 KeyMsg switch **앞에서** 분기(안 그러면 `q`·`j`·space가 목록·viewport로 간다): `ctrl+c` 끝, `Enter` 보내기, `Esc` 취소(입력 비우고 Blur), 나머지는 `textinput`으로. 보내는 중(`sending`)엔 `ctrl+c` 말고 전부 무시 — 보내는 동안 본문이 바뀌지 않게.
4. `Enter`: `strings.TrimSpace` 빈 본문이면 상태줄 "본문이 비었다". 아니면 `sending=true` + send Cmd: `PostMessageWithResponseContext(ctx, ch, text, [ts])` → 성공이면 `markRead(ctx, s, 응답 channel, 응답 ts)`(부모가 지워져 최상위로 갈 수 있으니 요청값 말고 응답값) → `sentMsg{ch, err, markErr}`.
5. `Update(sentMsg)`: `sending=false`.
   - 성공: 입력 닫음(재전송 막음), 미리보기 다시 받기(`preview(row)`). 읽음도 됐으면 그 줄 `done`, 읽음 실패면 ● 유지 + 상태줄 "보냈다 — 읽음 처리 실패: …"(`m`으로 다시).
   - Slack이 거절한 확실한 실패 — `slack.SlackErrorResponse`(`not_in_channel` 등), `*slack.RateLimitedError`, `slack.StatusCodeError` 4xx: 상태줄에 오류, 입력 유지.
   - 그 밖(타임아웃·네트워크·5xx): 상태줄 "전송 여부 불확실 — 미리보기 확인(Enter면 두 번 갈 수 있다)", 입력 유지, 미리보기 다시 받기.
   - 분류는 Update에서 한다(Cmd는 원본 오류만) — 테스트가 `sentMsg`를 직접 넣어 분류를 단정한다.

### `m` 치우기
1. ts = 그 줄 `latest.TS`, 단 마지막 미리보기가 같은 대화의 history였으면 그 최신 ts와 큰 쪽(`max`) — 미리보기로 본 것까지 치운다. `previewMsg`에 `latest`(history 응답의 최신 ts — Reverse 전에 잡는다. 스레드 미리보기는 "" — 답글 ts로 대화를 mark하면 사이의 안 본 채널 메시지까지 읽음이 된다)를 실어 성공한 Update만 `m.pv{ch, ts}`로 둔다.
2. mark Cmd: `markRead(ctx, s, ch, ts)` → `markedMsg{ch, err}`. 성공이면 그 대화 줄 `done`, 실패면 상태줄.
3. **`markRead`**: `markTo`를 부르고 "이미 더 뒤까지 읽음" 거절은 성공으로 본다. `markTo`가 내는 `usageError`는 그 거절뿐이라 `var u usageError; errors.As(err, &u)`로 가린다 — `markTo`에 주석으로 이 의존을 적는다. 답장 뒤 자동 읽음도 같은 helper.
4. 스레드(비공개 API라 TUI 안에서만 치우기)는 T3 — T2의 줄은 모두 history에서 와서(`thread_broadcast` 포함) `markTo`가 맞다.

### `o` 열기
- 빈 목록이면 무시(`m`도). 그 줄 `latest.Permalink`로 Cmd에서 `openURL(u)`. `openURL`은 변수(테스트가 바꾼다, `notifyFn`과 같은 방식): darwin이면 `exec.Command("open", u).Run()`, 아니면 오류. 오류면 상태줄 "열 수 없다 — <URL>"(다른 OS는 늘 이 경로).

## 화면
- 줄: `done`이면 `●` 대신 공백 + `lipgloss.NewStyle().Faint(true)`. `R`로 다시 채우면 치운 대화는 안 읽음에서 빠지므로 그대로 사라진다(T2에서 `done` 보존은 필요 없음). **알려진 빈틈**: `m`·답장 전에 이미 돌던 fill이 늦게 오면 치운 줄이 다음 `R`까지 ●로 돌아온다.
- 답장 중엔 아래 구분선 자리에 입력줄(` 답장 @이름 ↳> …`) — 높이 계산은 그대로. `textinput.CharLimit = 0`(제한 없음, 기본값). `WindowSizeMsg`에서 `ti.Width`를 맞춘다 — 0이면 긴 본문이 두 줄이 되어 높이가 밀린다.
- 키 힌트: `j/k 이동  Enter 열기  r 답장  m 치우기  o 열기(브라우저)  R 새로고침  q 끝`. 답장 중엔 `Enter 보내기  Esc 취소`.

## 테스트(tui_test.go, fakeSlack)
- `r`: 빈 목록이면 무시. DM 줄에서 `r` → 글자 입력 → Enter → Cmd 실행 → `chat.postMessage`에 `thread_ts` 없음, `conversations.mark` ts = 보낸 ts(`1700000099.000000`), 줄 `done`, 입력 닫힘.
- 대상 규칙 표 테스트: DM·그룹 DM/스레드 답글/답글 달린 부모(ThreadTS==TS)/public·private 채널 → `""`/ThreadTS/`""`(DM)/TS. `q`·`j`가 입력에 글자로 들어가는지.
- 빈 본문 Enter → Cmd 없음 + 상태줄. `Esc` → 입력 닫힘, 요청 없음.
- `sentMsg{err: SlackErrorResponse}` → 입력 유지 + 오류, 미리보기 Cmd 없음. `sentMsg{err: 네트워크}` → 입력 유지 + "불확실" + 미리보기 Cmd.
- `m`: D3UNREADDM 미리보기 뒤 `m` → mark ts가 미리보기 최신(`…05`). `last_read`보다 과거 ts 줄(C1GENERAL0 last_read `…02`, latest `…01`)에 `m` → `done`, `conversations.mark` 호출 없음(`conversations.info`는 불린다).
- `o`: `openURL` 바꿔치기(`t.Cleanup`으로 복원) → 받은 URL이 permalink. 오류면 상태줄에 URL.
- `textinput`의 Focus·글자 입력은 Blink Cmd를 낸다 — `cmd == nil` 단정은 빈 본문 Enter·Esc에서만.
- 완료 기준: `go test -race ./...`. 실제 보내기·mark·open은 라이브(사용자, 셀프 DM) — 스레드에 답장한 뒤 답글 ts로 `conversations.mark`가 받아지는지 포함.

## 안 하는 것(T2)
- 여러 줄 입력(`textarea`) — Slack 답장은 대개 한 줄. *(ponytail: 필요하면 textarea + ctrl+j)*
- 스레드만 TUI 안에서 치우기·`R` 뒤 `done` 보존 — T3(실시간 스레드 답글이 생길 때).
- 보낸 뒤 다른 줄로 커서 이동, 치운 줄 숨기기.
- `o`의 Linux `xdg-open` — 요청 오면.
