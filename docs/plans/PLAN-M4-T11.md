# M4 T11 구현 계획 — 이모지 단축코드 → 글자

> 2026-10-15 · 대상: TASKS M4 T11(새 항목) · 상태: 초안(리뷰 전)
> 목표(사용자 요청 "이모지들이 안보여요"): `:car: 9`, `… 진행할게요. :bow:`가 🚗 9, 🙇로. 사용자 결정: T7보다 먼저. 커스텀(워크스페이스) 이모지는 그림이라 `:이름:` 그대로 — T7에서 그림으로.

## 1. 이름표

- Slack이 쓰는 이름은 iamcal/emoji-data(npm `emoji-datasource`)의 `short_names` — 1903개 이모지, 이름 1964개(spike: `+1`/`thumbsup` → 1F44D, `bow` → 1F647, `heavy_check_mark` → 2714-FE0F).
- `internal/core/emoji.txt`(이름 TAB 글자, 한 줄에 하나, 약 40KB)를 `go:embed`로. 처음 쓸 때 map으로 한 번 푼다(`sync.OnceValue`). 새 모듈 없음.
- 만드는 스크립트 `internal/core/emoji.py`(`//go:generate uv run python emoji.py`): 고정 버전 `emoji-datasource@15.1.2`의 emoji.json을 받아 쓴다. 버전을 올릴 때만 다시 돌린다.

## 2. 바꾸는 곳

- `core.Emoji(s string) string`: `:이름:`(그리고 바로 뒤 `:skin-tone-2:`…`:skin-tone-6:` → U+1F3FB…1F3FF)을 글자로. 표에 없는 이름(커스텀)은 그대로. 정규식 `:([a-z0-9_+'-]+):(?::skin-tone-([2-6]):)?`.
- `core.Render`: 태그 밖 조각에 `Emoji` — `read`·목록 줄·알림이 같이 바뀐다. *ponytail: 코드 블록 안의 `:x:`도 바뀐다(Render는 코드 구간을 모른다) — 문제 되면*.
- TUI `mrkdwnSpans`: 코드·인라인 코드·태그를 떼어 둔 **뒤**, 강조(`*_~`) 전에 `Emoji` — 코드 안은 안 바뀌고, `heavy_check_mark`의 `_`가 기울임 판정에 안 들어간다.
- 리액션 꼬리표(`tuiLines`): `:car: 9` → `🚗 9`(`Emoji(":"+name+":")` — 리액션 이름에도 `::skin-tone-N` 꼴이 온다).
- rich_text 이모지 요소: `RichTextSectionEmojiElement.SkinTone`이 있으면 `:name::skin-tone-N:`로(지금은 버린다).

## 3. 폭

- 이모지는 2칸. `ansi.StringWidth`·`cellbuf.Wrap`이 2칸으로 센다. `2714-FE0F`(✔️)처럼 VS16이 붙은 것은 터미널마다 1칸·2칸이 갈려 줄 끝이 한 칸 밀릴 수 있다 — 표의 글자를 그대로 쓰고, 라이브에서 보고 정한다.

## 테스트

- `TestEmoji`(core): `:car:` → 🚗, `:+1::skin-tone-3:` → 👍🏼, `:no_such_custom:` 그대로, `12:30:45` 그대로, `:bow:` 두 개.
- `TestRender`: 태그 밖은 바뀌고 링크 안 `:x:`는 그대로.
- TUI: 본문 `:bow:` → 🙇, 코드 `` `:car:` `` 그대로, `:heavy_check_mark:` 기울임 없음, 리액션 `🚗 9`.

## 안 하는 것

- 커스텀 이모지(그림 — T7), 별칭 `emoji.list` API 조회, 이모지 폭 보정.
