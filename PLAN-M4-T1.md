# M4 T1 구현 계획 — `gslack tui` 보기(읽기 전용)

> 2026-10-13 · 대상: TASKS M4 T1, PRD-TUI.md §5 · 상태: 초안(리뷰 전)
> 목표: 밀린 대화 목록 + Enter 미리보기 + j/k/R/q. 쓰기·실시간 없음.

## 흐름
1. `tui` RunE: `--json`/`--raw`면 usage 오류. `a.channels(ctx, [im, mpim])`(세션 + 대화 캐시) → `s.loadUsers` — **프로그램 시작 전에** 끝낸다(이후 Cmd 고루틴은 캐시를 건드리지 않는다).
2. `tea.NewProgram(model, tea.WithContext(ctx), tea.WithAltScreen())`. `a.errw`를 상태줄 writer로 바꾼다(`p.Send(statusMsg)` — 폴백 경고 등이 화면을 깨지 않게. `isTTY`가 거짓이 되어 진행 표시는 자동으로 꺼진다).
3. `Init` → fill Cmd: `a.scanTargets(ctx, s, chans)`(아래 추출) → `filledMsg{items, err}`. API만.
4. `Update(filledMsg)`: `s.message`로 정규화(이름 해석은 여기서만) → 줄 `{ch, count, more, latest}`를 latest.TS 내림차순. 커서는 채널 ID로 유지.
5. `Enter` → preview Cmd: `latest`가 스레드 답글(ThreadTS≠TS)이면 `s.replies(ctx, ch, ThreadTS)`, 아니면 `conversations.history` limit 20(뒤집어 오래된 순) → `previewMsg{ch, msgs, err}`. Update에서 `printMessages`를 `bytes.Buffer`에 → `viewport.SetContent`. 늦게 온 다른 대화의 결과는 버린다(`ch` 비교). 대화별 캐시는 T1에서 안 함(Enter로만 받으니 폭주 없음).
6. `R` → fill Cmd 다시(진행 중이면 무시). `q`/`ctrl+c` → `tea.Quit`.

## 화면
- 위: 목록(높이의 40%, 커서 줄 반전). 줄: `● 이름(24폭) 개수 상대시간 미리보기` — 폭은 `runewidth.Truncate`(이미 간접 의존).
- 가운데: `viewport`(미리보기, `pgup/pgdn`·`space`로 스크롤).
- 아래 한 줄: 키 힌트 / 상태(채우는 중, 오류, 경고).
- 레이아웃은 문자열 조합 + lipgloss 최소(굵게·반전). 폭 분기 없음.

## 추출(리팩터)
- `unread.go`: RunE의 "session이면 client.counts로 줄이고 → scanUnread → nil 제거"를 `a.scanTargets(ctx, s, chans) ([]*unreadItem, error)`로. `unread`와 TUI가 같이 쓴다.
- `read.go`: `threadCmd`의 페이지 루프를 `s.replies(ctx, ch, ts) ([]slack.Message, error)`로.

## 의존성
- `github.com/charmbracelet/bubbletea` v1.3.10, `bubbles`(viewport만), `lipgloss`. v2(charm.land)는 모듈 경로가 바뀌어 v1 유지.

## 테스트
- 모델 단위(fakeSlack + `a.open`): fill Cmd를 직접 실행해 Update에 넣고 → 줄 순서·개수; `Enter` Cmd 실행 → previewMsg → View에 본문; 다른 대화의 늦은 previewMsg 무시; `R` 중복 무시; `q`가 Quit.
- `tui --json` 거절. 실제 TTY 실행은 라이브(사용자).

## 안 하는 것(T1)
- 쓰기(`r`/`m`/`o`)는 T2, 실시간은 T3.
- 채널 멘션 밀린 것: session의 `client.counts`는 채널별 `mention_count`를 주므로 "멘션 있는 채널"을 넣을 수 있다 — T1은 `unread` 기본과 같은 DM·그룹 DM만. 써 보고 T2/T3에서.
- 대화별 미리보기 캐시, 마우스, 리사이즈 외의 레이아웃 모드.
