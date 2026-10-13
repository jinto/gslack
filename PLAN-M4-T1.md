# M4 T1 구현 계획 — `gslack tui` 보기(읽기 전용)

> 2026-10-13 · 대상: TASKS M4 T1, PRD-TUI.md §5 · 상태: Codex·Fable 리뷰 반영
> 목표: 밀린 대화 목록 + Enter 미리보기 + j/k/R/q. 쓰기·실시간 없음.

## 흐름
1. `tui` RunE: `--json`이면 usage 오류(`--raw`는 root가 이미 거절). `!isTTY(a.out)`이면 usage 오류(`tui | less` 방지). `a.open(ctx)`만 하고 바로 프로그램을 띄운다.
2. `tea.NewProgram(model, tea.WithContext(ctx), tea.WithAltScreen())`. `p.Run()` **직전에** `a.errw`를 상태줄 writer(`send func(tea.Msg)`로 `statusMsg`를 보냄)로 바꾸고 `defer`로 되돌린다 — 안 되돌리면 RunE 오류가 끝난 프로그램으로 가서 사라진다. 교체~Run 사이에 errw에 쓰는 코드는 없다. `isTTY(a.errw)`가 거짓이 되어 진행 표시는 꺼지고 상태줄은 정적 "채우는 중…".
   - **규칙**: `Update` 안에서 `a.errw`에 쓰지 않는다(`Send`는 unbuffered라 Update 안에서 부르면 막힌다).
3. `Init` → 첫 fill Cmd: `a.channels(ctx, [im, mpim])` + `s.loadUsers` + `a.unreadRows(ctx, s, chans)` → `filledMsg{chans, items, err}`. 세션 캐시(`s.chans`·`s.users`)는 **이 Cmd만** 채우고, Update는 첫 `filledMsg` 전엔 세션을 건드리지 않는다(목록이 비어 Enter도 없음). 그래서 화면이 먼저 뜬다(PRD 성공 기준).
4. `Update(filledMsg)`: `filling=false`(오류여도). `chans`를 모델에 둔다. `s.message`로 정규화(이름 해석은 여기서만) → 줄 `{ch, count, more, latest core.Message}`. 순서는 `unreadRows`가 이미 정렬. 커서는 채널 ID로 유지, 없으면 0.
5. `Enter` → `m.seq++`, preview Cmd가 seq를 캡처: `latest`가 스레드 답글(`ThreadTS != "" && ThreadTS != TS`, core.Message 기준 — T1에선 `thread_broadcast`만 해당)이면 `s.replies(ctx, ch, ThreadTS)`, 아니면 `conversations.history` limit 20(뒤집어 오래된 순) → `previewMsg{seq, msgs, err}`. Update는 `msg.seq != m.seq`면 버린다(같은 대화 두 번 Enter·커서 이동 모두 덮음). 통과하면 `printMessages`를 `bytes.Buffer`에 → 머리줄(`#이름 — 스레드`/`#이름`) + `viewport.SetContent`.
6. `R` → `filling`이면 무시, 아니면 `filling=true` + fill Cmd(모델의 `chans`로 `unreadRows`만 — 세션 캐시 안 건드림). 한 번에 하나만 돌므로 세대 번호는 필요 없다. `q`/`ctrl+c` → `tea.Quit`.
7. `p.Run()` 오류가 `tea.ErrProgramKilled`면 `ctx.Err()`로 바꿔 돌려준다("program was killed" 문구 제거).

## 화면
- `tea.WindowSizeMsg` → 목록 높이(40%)·`vp.Width/Height` 갱신. 그 전엔 0(렌더는 됨).
- 위: 목록(커서 줄 반전). 줄: `● 이름 ↳ 시각 미리보기` — 이름은 `lipgloss.NewStyle().Width(24).MaxWidth(24)`로 자른다(runewidth 직접 의존 안 함). 시각은 `unread`와 같은 `01/02 15:04`(상대시간 helper는 안 만든다). 스레드 답글이면 `↳`.
- 가운데: `viewport`. `vp.KeyMap`은 `pgup`/`pgdown`·`space`만 — 기본 KeyMap의 `j/k/d/u/f/b/h/l`이 목록 키·T2의 `r/m/o`와 겹친다.
- 아래 한 줄: 키 힌트 / 상태(채우는 중, 오류, 경고).
- 키: `j/k`, `↑/↓`, `Enter`, `R`, `q`. 빈 목록이면 Enter·이동 무시.

## 추출(리팩터)
- `unread.go`: RunE의 "session이면 client.counts로 줄이고(실패면 경고 후 폴백, ctx 취소면 반환) → `scanUnread` → nil 제거 → `latest.Timestamp` 내림차순"을 `a.unreadRows(ctx, s, chans) ([]*unreadItem, error)`로. `unread` RunE는 이걸 부르고 정규화·출력만. 경고 문구·오류 그대로(기존 TestUnread·TestUnreadClientCounts가 회귀를 잡는다).
- `read.go`: `threadCmd`의 페이지 루프(Limit 200, cursor 반복, 반환 순서 그대로)를 `s.replies(ctx, ch, ts) ([]slack.Message, error)`로. 출력·`channelID` 해석은 threadCmd에 남는다(TestThread).

## 의존성
- `bubbletea` v1.3.10, `bubbles`(viewport만), `lipgloss`. 첫 import 후 `go mod tidy`로 직접 의존이 된다. v2(charm.land)는 모듈 경로가 바뀌어 v1 유지.

## 테스트
- 모델 단위(fakeSlack + `a.open`, View 전에 `WindowSizeMsg{80,24}`): fill Cmd를 직접 실행해 Update에 넣고 → 줄 순서·개수; fill 오류 뒤 `R`이 다시 동작; `Enter` Cmd 실행 → previewMsg → View에 본문; 낡은 seq의 previewMsg 무시; `R` 중복 무시; 빈 목록에서 Enter·j 무시; `q`가 `tea.QuitMsg`.
- `tui --json` 거절, 비TTY 거절.
- 완료 기준: `go test -race ./...` 통과. 실제 TTY 실행은 라이브(사용자).

## 안 하는 것(T1)
- 쓰기(`r`/`m`/`o`)는 T2, 실시간은 T3.
- "채우는 중 12/40" 진행 수: isTTY가 꺼져 정적 문구. *(ponytail: 필요하면 scanUnread 진행을 `\r` 없이 writer로)*
- 채널 멘션 밀린 것: `client.counts`의 `mention_count`로 넣을 수 있지만 T1은 `unread` 기본과 같은 DM·그룹 DM만.
- 대화별 미리보기 캐시, 마우스, 상대시간, 리사이즈 외의 레이아웃 모드.
