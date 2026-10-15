# M4 T6 구현 계획 — 미리보기 렌더링(Slack 느낌)

> 2026-10-15 · 대상: TASKS M4 T6(새 항목) · 상태: Codex·Opus 리뷰 반영(Fable은 한도로 Opus가 대신)
> 목표(사용자 요청): 미리보기가 Slack 원본처럼 보이게 — 봇 첨부의 색 막대·링크 제목·굵게·footer, 줄바꿈. 이미지는 T7(반블록 기본 + kitty/Ghostty 선명 + Quick Look 원본, 올린 이미지·봇 첨부 이미지·아바타 셋 다 — 사용자 결정 2026-10-15).
> 범위: **TUI 미리보기만**. `gslack read`는 평문 그대로(파이프·스크립트 입력, ts 열 유지).

## 1. 메시지 한 개 = 스타일 입힌 줄 목록

새 파일 `internal/cli/tuirender.go`: `(s *session) tuiLines(ctx, raw slack.Message, cm core.Message, width int) []string`. 미리보기는 메시지마다 이것을 부르고 줄을 이어 붙인다(날짜 머리줄은 지금처럼 날이 바뀔 때). T7은 이 결과 옆·아래에 그림 칸을 끼운다.

```
  11:25  LXP Deploy BOT  APP                       ← 시각 흐리게, 이름 굵게, 봇이면 APP 흐리게
         ▌ [RESOLVED] TDEA PROD Error Spike Detection   ← ▌는 첨부 color, 제목은 파랑+밑줄+OSC 8 링크
         ▌ TDEA PROD Error Spike Detection              ← *굵게* → ANSI 굵게(별표 없음)
         ▌ Service learning-agent has more errors in    ← 폭에 맞춰 줄바꿈
         ▌ the last hour than its 7 day hourly …
         ▌ Grafana v13.0.1+security-01 · 11:25          ← footer 흐리게
         [2 replies] (edited) :eyes: 3                  ← 꼬리표·리액션 흐리게
```

- **ts 열을 뺀다**(TUI만). 메시지는 `HH:MM  이름` 머리줄 + 들여쓴 본문. 같은 사람이 5분 안에 이어 쓰면 머리줄 생략(Slack과 같다) — *ponytail: 생략은 요청 오면. v1은 메시지마다 머리줄*.
- **본문 순서**(Slack과 같다): blocks가 있으면 blocks, 없으면 `text` → 그 뒤 attachments → 파일 꼬리표(`[file: a.png]`, T7에서 썸네일) → 리액션·꼬리표 한 줄.
- **blocks**: header는 굵게, section 텍스트·fields, context는 흐리게, divider는 `───`, rich_text는 지금 `richText` 그대로, image 블록은 `[image: alt]`(T7).
- **attachments**: `pretext`는 막대 밖, 그 아래 막대 안에 `author_name`(흐리게 굵게) · `title`(링크면 파랑 밑줄) · `text` · fields(`제목` 굵게 + 값) · `image_url`은 `[image]`(T7) · `footer · 시각`(흐리게). 막대 색은 `color`: `#rrggbb`/`rrggbb` 그대로, `good`/`warning`/`danger`는 Slack 색(#2eb886/#daa038/#a30200), 없으면 회색.

## 2. mrkdwn 스타일

- `core.Render`는 그대로 평문을 내고(`read`·목록 줄·알림이 쓴다), TUI는 그 결과에 스타일을 덧입힌다 — 단 링크만은 구조가 필요하다.
- **링크**: `core.Render`가 `<url|label>`을 `label (url)`로 펴서 TUI가 되돌릴 수 없다 → `Render`에 링크 훅 하나(`link func(url, label string) string`, nil이면 지금 규칙). TUI 훅은 `label`을 파랑+밑줄+OSC 8(`ansi.SetHyperlink`)로, label이 없으면 url을 그렇게. 호출하는 곳은 `session.render` 하나.
- **굵게·기울임·취소선·코드**: `*a*` `_a_` `~a~` `` `a` `` — Slack 규칙대로 단어 경계에서만(`2*3*4`는 그대로). 코드 블록(```` ``` ````)은 안쪽을 건드리지 않고 흐린 배경 없이 들여쓰기만. `>` 인용은 줄 앞 `│` 흐리게.
- 정규식 하나씩, 코드 구간을 먼저 떼어 둔 뒤 나머지에만.

## 3. 줄바꿈·폭

- 본문 폭 = 미리보기 폭 − 들여쓰기(7) − 막대(2). `ansi.Wrap`(charmbracelet/x/ansi — 이미 lipgloss가 끌어온 간접 의존성을 직접으로)으로 ANSI·한글 폭을 지키며 감싼다.
- 화면 폭이 바뀌면(WindowSizeMsg) 미리보기를 다시 렌더링 — 모델에 원본(`[]slack.Message`)과 `core.Message`를 둔다. 고른 메시지는 인덱스로 유지.
- 줄바꿈이 생겨도 `pvStarts`는 메시지마다 실제 줄 수를 더해 만든다(T5의 `printMessages` 반환값 대신 `tuiLines` 길이).

## 4. 고른 메시지 표시

- T5의 Reverse 덧입히기는 스타일 줄과 섞이면 깨진다 → **왼쪽 2칸 홈**에 `┃ `(고른 메시지 줄 전부). 미리보기 포커스면 강조색, 목록 포커스면 흐리게, 안 고른 메시지는 공백 2칸. `highlight()`는 홈만 바꿔 이어 붙인다.

## 5. 색

- `lipgloss.AdaptiveColor`로 밝은/어두운 배경 둘 다. 흐리게 = Faint 대신 회색 전경(Faint는 터미널마다 안 보이기도 한다), 링크 파랑, 강조(홈) = 목록 커서와 같은 계열.
- `NO_COLOR`·색 없는 터미널은 lipgloss가 알아서 평문 — 그때도 막대 `▌`·홈 `┃`은 글자라 구조가 보인다.

## 6. 리뷰 반영(Codex·Opus) — 위 §1~§5보다 이것이 우선

- **span 단위로 만들고 span 단위로 감싼다**(Codex 2·4, Opus 3·5): 평문을 만든 뒤 스타일을 덧입히지 않는다. TUI 전용 토크나이저가 mrkdwn 원문을 `span{text, style, link}` 목록으로 — 코드 블록·인라인 코드·`<…>` 태그를 먼저 떼고(태그는 `core.Render`로 이름을 풀어 평문 span, 링크는 link span), 나머지에서만 `*` `_` `~` 마커를 단어 경계로(Go 정규식엔 lookbehind가 없어 앞뒤 글자를 직접 본다. `_`는 앞뒤가 공백·문장부호일 때만 — `snake_case_name` 그대로). 그 뒤 **줄바꿈은 span을 단어 단위로 채워 줄마다 각 조각을 스타일로 렌더링** → 줄을 넘는 굵게·링크도 줄마다 온전하고, `ansi.Wrap`의 SGR 이어짐 문제와 URL 안 `_b_` 오탐이 없다. → §2의 "Render 결과에 덧입힘"과 "core.Render 링크 훅"은 **없앤다**(core는 그대로).
- **OSC 8 안전**(Codex 3): link span은 URL·label에서 C0 제어문자·DEL·C1(0x80–0x9f)을 지우고, 스킴이 `http`/`https`/`mailto`일 때만 하이퍼링크. 아니면 평문.
- **text vs blocks**(Opus 2): `raw.Text`를 쓴다(`cm.Text`는 attachments fallback이 들어가 두 번 찍힌다, Opus 1). 블록이 rich_text뿐이면 text, header·section·context·divider·image가 있으면 blocks, 블록이 아무것도 안 그리면 text. rich_text 메시지의 text에는 mrkdwn 마커가 그대로 있어 스타일이 산다.
- **attachments**: `fallback`(pretext·title·text가 다 비면), `attachment.Blocks`(같은 블록 렌더러를 막대 안에서), `HideColor`면 막대 없음. 색은 trim·소문자·`#` 떼고 6자리 hex만, `good/warning/danger`, 그 밖은 회색.
- **색**: UI는 ANSI 16색만(회색 `8`, 파랑 `4`, 강조 `6`) — `AdaptiveColor`는 첫 사용 때 OSC 11로 배경을 물어 bubbletea 입력과 엉킨다(Opus 6). 막대만 hex.
- **폭**: 홈(2) + 들여쓰기(상수 `bodyIndent`) + 막대(첨부면 2)를 접두사 문자열로 두고 `ansi.StringWidth`로 뺀다. 테스트는 최종 줄 폭 ≤ `vp.Width`. 감싼 줄은 viewport 폭 안이라 가로 자르기가 일어나지 않는다(링크가 잘리지 않음).
- **날짜 줄 판별**: `"── "` 접두어 검사를 없애고 메시지마다 `pvTops`(날짜 줄이 있으면 그 줄, 없으면 첫 줄)를 기록. `pvSpan`의 끝 = 다음 메시지의 top. 날짜 줄은 흐리게 칠해도 된다.
- **폭 바뀜**: `msg.Width != vp.Width`일 때만 다시 렌더링(드래그 중 연달아 옴), 모델에 `pvRaw []slack.Message`만 더 두고(`core.Message`는 `pvMsgs`), 다시 그린 뒤 `showSel()`.
- **T7 대비**(Opus 10·11): `tuiLines`는 네트워크를 안 쓴다(이름 캐시 빼고) — 이미지는 Cmd에서 받는다. `[image]`·`[file]` 자리표시는 한 줄. kitty/Ghostty는 유니코드 자리표시자(U+10EEEE) 방식이어야 `[]string` 줄·viewport 스크롤을 그대로 쓴다. 들여쓰기는 상수 하나(아바타가 들어오면 바뀐다).
- `printMessages`의 시작 줄 반환(T5)은 TUI가 안 쓰게 되니 되돌린다.
- 테스트의 색 프로필은 전역이라 `lipgloss.SetColorProfile` 후 `t.Cleanup`으로 복원(병렬 없음).

## 테스트

- `TestTUILines`(색 없는 프로필로 구조, 트루컬러 프로필로 ANSI 하나씩):
  - 위 Grafana 첨부(color `#2eb886`, title_link, text에 `*굵게*`, footer, ts) → 줄 순서, 막대, 별표 없음, footer에 시각.
  - blocks(header·section·context·divider) 순서. text와 blocks가 둘 다면 blocks.
  - 폭 30에서 긴 한글·영문 줄바꿈 — 모든 줄 `ansi.StringWidth ≤ 폭`.
  - 링크: OSC 8 시퀀스와 label, label 없으면 url.
- `TestMrkdwnSpans`: `*a*`·`_a_`·`~a~`·`` `a` ``·코드 블록 안 그대로·`2*3*4`·`snake_case_name` 그대로·`> 인용`·`<url|label>` link span·`<@U…>` 이름·URL 안 `_b_` 그대로.
- 줄 넘는 굵게·링크: 둘째 줄에도 SGR·OSC 8이 있다. 제어문자 든 URL → 하이퍼링크 없음. `javascript:` → 평문.
- text 비고 attachments만 → 본문 한 번. rich_text + text → text의 굵게. fallback만 있는 첨부. HideColor.
- T5 테스트: 강조 단언을 홈(`┃`)으로, ts 대신 시각으로. 폭 바뀜 → 다시 렌더링, 고른 메시지 유지.

## 안 하는 것(T6)

- 이미지(T7), 이모지 단축코드 → 유니코드(의존성 필요 — 요청 오면), 같은 사람 연속 메시지 머리줄 생략, 목록 줄 스타일, `read` 출력.
