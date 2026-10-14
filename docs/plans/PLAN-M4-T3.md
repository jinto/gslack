# M4 T3 구현 계획 — `gslack tui` 실시간

> 2026-10-14 · 대상: TASKS M4 T3, PRD-TUI.md §2·§4·§5 · 상태: Codex·Fable 리뷰 반영
> 목표: TUI가 켜져 있는 동안 나에게 온 새 메시지(DM·그룹 DM·멘션·내 스레드 답글)가 받은편지함에 들어온다. watch 소스를 콜백으로 바꿔 그대로 쓰고, 재연결 때 자동 `R`, `--notify`.

## 1. watch 리팩터

- **`a.newWatcher(ctx, s) (*watcher, error)`**: watchCmd RunE의 `loadChannels(force)` → `member` → `myGroups` → `loadUsers`를 뽑는다. watch는 그 뒤에 `targets`·`all`만 채운다.
  - **동작 변경 하나**: `myGroups`의 모든 오류는 지금의 missing_scope처럼 경고 + `groups=nil`(모든 그룹 멘션 통과), `loadUsers` 실패도 경고(이름은 `users.info` 폴백). `loadGroups`는 오류를 기억하므로(session.go:347) 한 번의 429가 TUI를 끝까지 막지 않게. watch도 이 두 경우 종료 대신 경고로 바뀐다.
- **`liveDialer(s) (*websocket.Dialer, error)`**: RunE 앞쪽의 분기 그대로 — session이면 `rtmDialer`, 아니면 `s.app == ""`일 때 usage 오류, 아니면 `nil`(Socket Mode). 네트워크 없음.
- **`a.listen(ctx, s, dialer, on func(*slackevents.MessageEvent) error, reconnected func()) error`**: `dialer != nil`이면 `watchRTM`, 아니면 `watchSocket`. 두 소스는 `a.emit(…)` 대신 `on(e)`, 재연결 출력 대신 `reconnected()`를 부른다. `w`·`notify` 인자는 소스에서 빠진다.
- watch: `on = func(e) error { return a.emit(ctx, s, w, e, notify) }`, `reconnected`는 지금 문구를 `a.errw`로.
- `keep` 맨 앞에 `e.Message == nil`이면 `false, false`(지금은 `emit`만 막는다 — 두 경로 다 덮게).

## 2. TUI에 소스 붙이기

- **RunE에서**: `--notify`(macOS만, watch와 같은 검사), `dialer, derr := liveDialer(s)`. usage 오류(앱 토큰 없음)면 `m.live = "실시간 꺼짐: 앱 토큰(xapp-) 없음"`으로 소스 없이 연다. 그 밖의 오류(쿠키 없음)는 RunE 오류.
- **첫 채우기 Cmd**(`chans == nil` 갈래): `w, err := a.newWatcher(ctx, s)` → 성공하면 DM·그룹 DM `chans`를 고르고, 소스가 있으면 `go func() { push(liveDownMsg{a.listen(ctx, s, dialer, on, reconnected)}) }()` → `unreadRows`. 소스를 채우기 **전에** 띄워 채우는 동안 온 메시지도 잡는다.
  - `unreadRows`가 실패해도 `filledMsg`에 `chans`를 싣고, Update는 오류와 무관하게 `m.chans`를 먼저 받는다 — **불변식: `newWatcher`·`listen`은 한 번만**(`m.chans == nil`일 때만 그 갈래로 가고, 그 갈래는 `newWatcher`가 실패했을 때만 다시 탄다 — 그때는 소스도 안 떴다).
- **콜백 — `keep`은 소스 고루틴에서**: `on = func(e) error { if _, alert := w.keep(e, …); alert { push(liveMsg{e}) }; return nil }`. `keep`은 `w`와 이벤트만 읽는다(세션 안 건드림). RTM은 가입 대화의 **모든** 메시지를 주는데, Update(캐시 미스면 `users.info`)를 거치지 않고 소스에서 걸러 `IncomingEvents`(버퍼 50)가 밀리지 않게. *PRD §4 "keep은 Update에서"와 다르다 — 그 규칙의 이유(세션 캐시 경합)는 `keep`에 해당하지 않는다.* 받은편지함 = `alert`(`show`가 아니라 — TUI watcher에는 `all`·`targets`가 없다).
- `reconnected = func() { push(reconnectedMsg{}) }`. `push`는 모델 필드(`send`는 이미 메서드) — tuiCmd가 `NewProgram` 뒤 `m.push = p.Send`(`ta.errw`와 같은 방식). 테스트는 버퍼 있는 `chan tea.Msg`(소스 고루틴이 부른다 — `-race`).
- **liveDownMsg**: `err == nil`이거나 `context.Canceled`(q로 끝남)면 무시, 아니면 `m.live = "실시간 꺼짐: " + err`. 화면은 계속.
- **동시성(왜 잠금 없이 되나)**: (a) 첫 채우기 Cmd의 캐시 쓰기(`newWatcher`)는 소스를 띄우기 전에 끝난다 → 첫 `liveMsg`보다 먼저, (b) `unreadRows`·R 채우기는 캐시를 안 쓴다, (c) 소스 고루틴은 시작 때 `s.chans`를 한 번 읽고(RTM `kind`) 세션을 쓰지 않는다. 그래서 Update는 `liveMsg`를 받자마자 `s.message` 등을 불러도 된다. 나중에 R에서 `loadChannels`/`loadUsers(force)`를 부르면 이 셋이 깨진다 — 코드 주석으로.

## 3. 병합(Update, `liveMsg`)

`m.apply(e, notify bool) tea.Cmd`: 대화 ID로 줄을 찾는다.
- 없으면 새 줄 `count=1`. 대화는 `s.chans`에서 ID로, 없으면(실행 중 새 DM, 가입하지 않은 채널의 `<@나>`) `core.Channel{ID, Name: channelName 폴백 ID, Kind: e.ChannelType, User: im이면 e.User}` — `im`만 특별 취급(이름·답장 규칙이 보는 건 `KindIM`·`KindMPIM`뿐, RTM은 core Kind, Socket Mode는 `channel`/`group`이지만 둘 다 "채널"로 다뤄진다).
- 있으면 `ts > latest.TS`일 때만 바꾼다: `done`이었으면 `count=0`부터(치운 뒤 새 것만 센다), `count+1`·`latest`·`text` 교체·`done=false`. 아니면 무시(채우기와 겹친 메시지·재전송).
- 바뀌었으면 latest 내림차순 재정렬(`SortStableFunc`), 커서는 대화 ID를 따라간다.
- 줄 만들기(이름·`text`·`s.message`)는 filledMsg와 같은 helper `m.row(ch, count, more, raw)` — filledMsg의 인라인을 뽑는다. IM 이름은 `ch.User`가 비면(봇 DM) `latest.UserName`.
- `notify && m.notify`이고 바뀌었으면 Cmd에서 `notifyFn(이름, preview(text, 120))`, 실패는 상태줄.
- `m.filling`이면 `m.pending`에도 넣는다(아래).

## 4. 채우기 결과와 합치기(`filledMsg`, `R`)

1. 채우기로 새 줄을 만든다.
2. 옛 줄 중 `!done`이고 **채우기가 못 보는 줄** — `thread()`이거나 대화가 `chans`(채우기 대상 = 시작 때의 DM·그룹 DM)에 없음 — 은 남긴다. 같은 대화가 새 줄에도 있으면 `latest`가 새 쪽. 실행 중 새로 생긴 DM·채널 멘션은 여기 걸려 남는다(Slack 앱에서 읽어도 재시작까지 ● — PRD §2 빈틈과 같은 부류). 치운 줄은 남기지 않는다: DM은 이미 읽음이라 채우기에도 없고, 스레드는 TUI 안에서 치운 것이니 사라져도 같다.
3. `m.pending`을 `notify=false`로 다시 적용하고 비운다(처음 적용 때 이미 알렸다). 채우기가 그 메시지를 봤으면 채우기 `latest ≥ ts`라 ts 가드가 거르고, 못 봤으면(그 대화를 먼저 훑음) `+1` — 채우기의 `count`는 그 시점 history라 둘 중 하나만 맞다.
4. 실패한 채우기도 `m.chans`를 받고 2·3을 한다(줄은 옛 줄 그대로 두고 pending만 비운다).
5. `m.refill`이면(채우는 중 재연결) 내리고 `m.fill()` 한 번 더.

## 5. 재연결 → 자동 R

- `reconnectedMsg`: `m.filling`이면 `m.refill = true`(그 채우기가 끊긴 시점 전에 그 대화를 훑었을 수 있다), 아니면 `m.fill()` 뒤 상태줄 "다시 연결됨 — 새로 채운다"(`fill()`이 상태를 비우므로 뒤에). 끊긴 사이의 스레드 답글·채널 멘션은 못 되찾는다 — watch와 같은 빈틈.

## 6. 스레드·채널 줄의 읽음

- `m`: `r.thread()`면 API 없이 그 줄만 `done`(PRD §3). 답글 ts로 대화를 mark하면 사이의 안 본 채널 메시지까지 읽음이 된다. 채널 멘션 줄(스레드 아님)은 지금처럼 `markRead(ch, latest.TS)`.
- `r` 뒤 자동 읽음도 같은 이유로 고친다: `replyThread(r) == ""`(DM 본문)면 지금처럼 보낸 ts, `thread()`면 mark 없이 `done`, 채널 줄(그 메시지에 스레드로 답함)이면 `latest.TS`까지. → T2의 "스레드 답장 뒤 답글 ts로 mark" 라이브 확인 항목이 없어진다.

## 7. 화면·플래그

- `gslack tui --notify`. Short: "받은편지함 화면 — 밀린 DM·그룹 DM과 실시간 DM·멘션을 보고, 답하고(r), 치운다(m)".
- 상태줄 앞에 `m.live`(꺼졌을 때만). 빈 목록 문구 → "나에게 온 것이 없다".

## 테스트

- 기존 watch 테스트 그대로 통과(리팩터 회귀). groups 오류가 경고로 바뀐 것 하나(있으면 고친다).
- `TestTUILive`(fakeSlack, 채운 모델, `liveMsg` 직접):
  - 새 DM → 새 줄 `count=1` 맨 위, 커서는 원래 대화. 더 새 ts → `count+1`. 같은/옛 ts → 그대로. done 줄 + 새 ts → `count=1`, ● 다시.
  - `<@나>` 채널 이벤트 → 채널 줄, `replyThread` = `latest.TS`.
  - 채우는 중 이벤트 → 바로 보이고, filledMsg(그 ts를 latest로 가진 채우기 / 못 본 채우기) 뒤 count가 각각 맞음, 알림은 한 번.
  - R 합치기: 채널·스레드·`chans` 밖 DM 줄(!done) 남음, 치운 스레드 줄·`chans` 안이고 채우기에 없는 DM 줄 사라짐.
  - 첫 채우기 실패(`filledMsg{chans, err}`) 뒤 R → 다시 `newWatcher` 안 탐(fill Cmd가 첫 갈래가 아님).
  - 스레드 줄 `m` → `done`, `conversations.mark` 없음. 스레드 줄 `r` → mark 없음 + `done`. 채널 줄 `r` → mark ts = `latest.TS`.
  - `reconnectedMsg` → fill Cmd / 채우는 중이면 nil + filledMsg 뒤 fill Cmd. `liveDownMsg{err}` → 상태줄 `실시간 꺼짐`, `Canceled`는 무시.
  - `--notify`: `notifyFn` 바꿔치기 → 이벤트 하나에 한 번.
- `TestWatchKeep`에 `Message == nil` 한 줄.
- `TestTUIRTM`(e2e 한 개): session 프로필, `f.rtm`에 걸러질 채널 메시지 + DM → 첫 채우기 Cmd 실행 → `push` 채널에서 `liveMsg`가 DM 하나만. ctx 취소로 소스 종료.
- 완료 기준: `go test -race ./...`. 실제 RTM·Socket Mode에서 줄이 들어오는지, 재연결 뒤 자동 R은 라이브(사용자).

## 안 하는 것(T3)

- 켜짐 표시(`RTM ●`) — 꺼졌을 때만. *(ponytail: 요청 오면)*
- R에서 새 DM 찾기(`chans` 다시 읽기) — 실시간이 잡는다. 놓친 건 재시작.
- 열어 둔 미리보기 자동 갱신, RTM `channel_marked`로 흐리게, 한 채널 스레드 따로 줄(PRD Q1), 끊긴 사이 스레드 답글 되찾기.
