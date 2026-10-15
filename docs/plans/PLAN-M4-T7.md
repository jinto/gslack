# M4 T7 구현 계획 — 미리보기 이미지

> 2026-10-15 · 대상: TASKS M4 T7 · 상태: 초안(리뷰 전)
> 목표(사용자 요청 "이미지 표시도 필요한 기능인데.."): 미리보기에 그림을 보인다.
> 사용자 결정(2026-10-15): 터미널은 "여러 개 / 모름" → 어디서나 되는 기본 + 되는 곳은 선명하게. 대상은 **올린 이미지 파일 · 봇 첨부 이미지(`image_url`) · 아바타/봇 아이콘** 셋 다.
> 범위: TUI 미리보기만. `read`는 그대로.

## 1. 그리는 방식 셋 — 자동 선택, 환경변수로 덮어쓰기

| 방식 | 언제 | 어떻게 |
|---|---|---|
| **kitty** | `TERM=xterm-kitty`, `KITTY_WINDOW_ID`, `TERM_PROGRAM=ghostty` (단 `TMUX`면 아님) | kitty 그래픽 프로토콜 **유니코드 자리표시자**(`U=1` 가상 배치 + 칸마다 `U+10EEEE`+행·열 결합 문자, 전경색 = 이미지 ID). 줄이 그냥 글자라 `[]string`·viewport 스크롤·홈(┃)이 그대로 된다 |
| **half** (기본) | 그 밖 | `▀` 한 칸 = 위아래 두 픽셀(전경 = 위, 배경 = 아래). lipgloss로 칠해 색 없는 터미널·256색은 lipgloss가 알아서 낮춘다 |
| **off** | `GSLACK_IMAGES=off` | 지금(T6)처럼 `[image]`·`[file: …]` |

- `GSLACK_IMAGES=half|kitty|off`로 덮어쓴다(감지가 틀릴 때). iTerm2 OSC 1337·sixel은 안 쓴다 — 그림이 글자 칸 밖이라 bubbletea가 줄을 다시 그릴 때 깨진다.
- kitty 전송(APC `\x1b_G…\x1b\\`)은 View 밖에서 터미널에 직접 쓴다: 프로그램 출력(`a.out`)을 **잠금 writer**로 감싸 bubbletea 렌더러(한 프레임 = Write 한 번)와 우리 전송이 섞이지 않게. 잠금 writer는 `*os.File`을 품어 `Fd()`(창 크기·raw 모드)를 그대로 내고 `Write`·`WriteString`만 잠근다. 전송은 PNG(`f=100`, 디코드한 그림을 stdlib `image/png`로 다시 인코딩), base64 4096바이트씩 `m=1` 조각, `q=2`(응답 없음 — 입력에 섞이지 않게). 끝날 때 `a=d,d=I`로 우리 ID만 지운다.

## 2. 무엇을 어디에

- **올린 이미지**: `f.Thumb360`(없으면 `[file: …]` 그대로 — PDF 등). 메시지당 `[file: name]` 줄 자리에 그림, 그 아래 흐린 `name` 한 줄.
- **봇 첨부**: `a.ImageURL`(없으면 `a.ThumbURL`) → 막대 안 `[image]` 자리에 그림. image 블록(`[image: alt]`)도 같은 규칙.
- **아바타**: 사용자 `profile.image_48`(→ `core.User.Image`, users 캐시에 새 필드 — 옛 캐시는 비어 있어 TTL이 끝나 다시 받을 때까지 아바타 없음), 봇은 `bot_profile.icons.image_48`(없으면 메시지 `icons`). 머리줄과 그 다음 줄의 들여쓰기 칸 왼쪽 4칸×2줄에 그린다(Slack처럼 이름 왼쪽):
  ```
  ▀▀▀▀   김민수  11:25          ← 머리줄을 "이름 시각"으로(아바타가 앞에 오므로)
  ▀▀▀▀   본문 첫 줄
         본문 둘째 줄
  ```
  메시지가 머리줄 한 줄뿐이면 아바타 둘째 줄을 위해 빈 줄을 둔다. 들여쓰기(`bodyIndent`, 7칸)는 그대로 — 아바타 4칸 + 공백 3칸. 아바타를 아직 못 받았거나 off면 지금처럼 빈 칸.
- **크기**: 그림은 최대 40칸 × 12줄(아바타 4×2), 가로세로 비율 유지(칸은 1:2 — half는 한 칸 = 1×2 픽셀이라 픽셀이 정사각형, kitty는 `c`·`r`로 칸 상자를 주면 터미널이 맞춘다). 미리보기 폭이 그림 + 들여쓰기보다 좁으면 폭에 맞춰 줄인다(half는 다시 칠하고, kitty는 같은 ID에 새 배치 `c`·`r`로 다시 전송 — *ponytail: 폭마다 다시 전송, 드래그 중 많으면 마지막 폭만*).
- 축소는 stdlib만: 칸마다 원본 상자 평균(box filter, 몇 줄). 새 모듈 없음. 디코드는 `image/png`·`image/jpeg`·`image/gif`(첫 프레임) — WebP 등 모르는 형식은 자리표시 그대로.

## 3. 받기 — tea.Cmd, 메모리 캐시

- `tuiLines`는 지금처럼 네트워크를 안 쓴다. 그림 자리에 쓸 그림을 **모델의 캐시**(`m.imgs map[string]*pvImage` — URL → 디코드한 그림·kitty ID)에서 찾고, 없으면 자리표시를 그린 뒤 URL을 모은다. → `tuiLines(ctx, raw, cm, width, img func(url) *pvImage)`처럼 찾기 함수를 넘기고, 못 찾은 URL은 그 함수가 기억한다.
- `showPreview` 뒤 모은 URL마다 Cmd 하나(`imageMsg{url, img, err}`) — 한 번에 많아야 메시지 20개 × 몇 장이라 `tea.Batch` 그대로. 받으면 캐시에 넣고(kitty면 그때 전송) **미리보기를 다시 그린다**(`renderPreview` + `highlight`, 고른 메시지·스크롤 위치 유지 — 그림이 위에서 커지면 YOffset을 그만큼 밀어 보던 줄이 안 움직이게). 실패도 캐시에 기억(nil)해 다시 받지 않는다.
- 같은 URL을 받는 중이면 또 보내지 않는다(받는 중 표시). 캐시는 세션 동안 메모리에만 — *ponytail: 디스크 캐시는 느리다는 말이 나오면*.
- **인증·보안**:
  - 토큰은 Slack 파일(`files.slack.com`, `isSlackURL`)에만: 올린 이미지는 지금 `download`가 쓰는 `s.files.GetFileContext`(인증, 리다이렉트 검사 그대로). 아바타·봇 첨부(`image_url`은 아무 호스트)는 **토큰 없는 별도 http.Client**(쿠키 없음, 타임아웃 10초) — 인증 클라이언트로 외부 URL을 받으면 토큰이 샌다.
  - http·https만. 응답은 `io.LimitReader` 10MB, `image.DecodeConfig`로 먼저 크기를 보고 한 변 > 8192면 버린다(압축 폭탄).
  - 봇 `image_url`을 직접 받으면 그 호스트가 내 IP·시각을 안다(Slack 앱은 자체 프록시로 받는다). 사용자 요청이라 받되 계획에 적어 둔다 — `GSLACK_IMAGES=off`로 끈다.
- 텍스트 안의 `U+10EEEE`(자리표시 문자)는 `clean()`이 지운다 — 남이 보낸 글자가 내 그림을 흉내 내지 못하게.

## 4. Quick Look — 원본 보기

- 미리보기 포커스에서 `v`: 고른 메시지의 그림 원본을 연다.
  - 올린 이미지: `URLPrivateDownload`를 임시 디렉터리(세션에 하나, `os.MkdirTemp`, 끝날 때 지움)에 지금 `s.save`로 받아(0600) **macOS면 `qlmanage -p 파일…`**(여러 장이면 한 번에 — 넘겨 보기), 그 밖이면 `o`와 같은 여는 명령. 받는 동안 상태줄 "받는 중…".
  - 봇 첨부 이미지: URL을 브라우저로(`o`의 여는 함수).
  - 그림이 없으면 상태줄 "그림 없음".
- `qlmanage`는 백그라운드(`Start`, 출력 버림) — TUI를 막지 않는다. 키 힌트에 `v 그림`(그림 있는 메시지를 골랐을 때만).

## 5. 바뀌는 곳

- `internal/cli/tuiimage.go`(새): 감지, 받기(Cmd), 디코드·축소, half 칠하기, kitty 전송·자리표시 줄, 잠금 writer.
- `tuirender.go`: 그림 자리(파일·첨부·image 블록·아바타)에서 찾기 함수 사용, 머리줄 "이름 시각", `clean`에 U+10EEEE.
- `tui.go`: `m.imgs`·받는 중·`imageMsg`, 다시 그리기, `v`, 끝날 때 kitty 지우기·임시 디렉터리 지우기, `NewProgram`에 잠금 writer.
- `core.User.Image`(`json:"image,omitempty"`), `userFrom`에서 `Profile.Image48`. `--json` 출력에 필드가 하나 는다.

## 테스트

- `TestHalfBlock`: 2×4 픽셀(위 빨강, 아래 파랑) → 1칸×2줄? 아니 2칸×2줄, 칸마다 `▀` 전경 빨강·배경 파랑(트루컬러 프로필). 폭 줄이면 비율 유지.
- `TestKittyLines`: ID 0x000102, 3칸×2줄 → 줄마다 `\x1b[38;2;0;1;2m` + `U+10EEEE`+행 결합 문자…, `ansi.StringWidth` = 3. 전송 문자열: `a=T,U=1,f=100,i=…,c=3,r=2,q=2`, 4096 넘는 데이터는 `m=1` 조각.
- `TestImageFetch`(fakeSlack에 그림 경로): 올린 이미지는 Authorization 헤더가 있고, 외부 URL(테스트 서버 둘째)은 **헤더·쿠키 없음**. 10MB 넘음·8192 넘는 그림·깨진 데이터 → nil, 다시 안 받음.
- `TestTUIPreviewImages`: previewMsg(파일 메시지 + 첨부 image_url) → Cmd가 URL마다 하나, 같은 URL 둘이면 하나. `imageMsg` 받으면 자리표시가 그림 줄로, 고른 메시지·보던 줄 그대로. off면 Cmd 없음.
- 아바타: 사용자 Image 있음 → 머리줄·둘째 줄 왼쪽 4칸이 그림, 한 줄 메시지는 빈 줄 추가.
- `clean`: U+10EEEE 지움. `v`: qlmanage 대신 테스트용 여는 함수(지금 `o` 테스트 방식대로)로 받은 경로들 확인.
- 잠금 writer: `Fd()`가 그대로, `WriteString`도 잠금을 거친다.

## 안 하는 것

- 움직이는 GIF(첫 프레임만), 이모지 이미지(커스텀 이모지), 디스크 캐시, tmux 안의 kitty(패스스루), iTerm2·sixel, 그림 클릭.
