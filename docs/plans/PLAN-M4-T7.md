# M4 T7 구현 계획 — 미리보기 이미지(반블록)

> 2026-10-15 · 대상: TASKS M4 T7 · 상태: 리뷰 반영(Codex·Opus — Fable 한도)
> 목표(사용자 요청 "이미지 표시도 필요한 기능인데.."): 미리보기에 그림을 보인다.
> 사용자 결정(2026-10-15, 리뷰 뒤): v1은 **반블록만** — 올린 이미지·봇 첨부(`image_url`)·image 블록. kitty·아바타·Quick Look `v`는 T7b. 외부 그림은 **https 아무 호스트, 사설 IP 차단**. 커스텀 이모지는 T7 밖(`:name:` 그대로 — `emoji:read`를 c.4에서 뺐고 2칸이면 안 보인다).
> 범위: TUI 미리보기만. `read`·`--json`은 그대로(`core` 모델 안 바꿈).

## 1. 그리기 — 반블록

- `▀` 한 칸 = 위아래 두 픽셀(전경 = 위, 배경 = 아래) — 픽셀이 정사각형. lipgloss 색(`#rrggbb`)으로 칠해 256색·색 없는 터미널은 lipgloss가 낮춘다.
- 투명(알파 < 128): 둘 다면 공백, 위만이면 `▄`(전경 = 아래), 아래만이면 `▀`(전경 = 위)만 — 테마 배경이 비친다.
- 크기: 최대 40칸 × 12줄(= 40×24px), 미리보기 폭(들여쓰기·막대를 뺀)이 좁으면 거기에 맞춘다. 비율 유지, 키우지 않는다.
- 축소는 stdlib만 — 칸 픽셀마다 원본 상자 평균(box filter). 새 모듈 없음. 디코드는 `image/png`·`image/jpeg`·`image/gif`(첫 프레임만 읽는다). 모르는 형식(WebP 등)은 자리표시 그대로.
- `GSLACK_IMAGES=off`면 받지도 그리지도 않는다(외부 호스트에 IP를 알리기 싫을 때).

## 2. 무엇을 어디에

- **올린 이미지**: `raw.Files`(`slack.File` — `core.File`은 늘리지 않는다)의 `Thumb360`(+`Thumb360W/H`). 없으면(PDF 등) 지금처럼 `[file: name]`. 그림 아래 흐린 `name` 한 줄.
- **봇 첨부**: `a.ImageURL`(+`ImageWidth/Height`, 없으면 0) — 막대 안 `[image]` 자리.
- **image 블록**: `b.ImageURL`(크기 없음) — `[image: alt]` 자리. 첨부 안 블록도 같은 함수라 같이 된다. context 블록 아이콘은 그대로 뺀다.
- **자리 잡기**: 크기를 알면(파일·대부분의 unfurl) 받기 전부터 같은 줄 수를 잡는다 — 첫 줄 흐린 `[image]`, 나머지 빈 줄. 그림이 와도 줄 수가 안 바뀌어 스크롤 보정이 없다. 크기를 모르면(image 블록 등) 받기 전 한 줄, 받은 뒤 그림 크기로 — *ponytail: 위쪽 그림이 커지면 보던 줄이 밀린다, 불편하면 고른 메시지 기준 앵커*. 실패면 한 줄 자리표시.

## 3. 받기 — tea.Cmd, 메모리 캐시

- 캐시는 모델의 `m.imgs map[string]pvImage`(URL → `{img image.Image; done bool}`) — 키가 있으면 받는 중이거나 끝(실패면 `done`·`img nil`), 다시 안 보낸다. 세션 동안 메모리만. 디코드 직후 최대 상자(40×24px)로 줄여 두고 원본은 버린다(폭이 바뀌면 작은 것에서 다시 줄인다).
- `tuiLines(ctx, raw, cm, width, imgs)` — 찾기만, 네트워크 없음. URL 모으기는 그리기와 따로: `imageURLs(raw)`가 메시지에서 뽑는다. 메시지가 바뀌는 곳은 `showPreview`뿐이라 Cmd는 거기서만(`showPreview`가 `tea.Cmd` — `tea.Batch`). 폭 변경·T8 Esc 복원은 캐시로 그린다(T8 스레드 미리보기도 `showPreview`라 같이 된다).
- `imageMsg{url, img, err}`: 캐시에 넣고, `len(m.pvMsgs) > 0`일 때만(불러오는 중·실패 화면을 덮지 않게) `renderPreview` + `highlight`. YOffset은 그대로.
- **인증·보안**:
  - **토큰은 올린 이미지 썸네일에만**: `raw.Files`의 `Thumb360`이고 호스트가 `isSlackURL`(테스트는 `apiURL`)일 때 `s.files.GetFileContext`(쿠키·토큰, `checkRedirect` 그대로). 그 밖은 Slack 호스트여도 토큰 없이 — 보낸 사람이 고른 `image_url=https://slack.com/api/…`에 내 토큰으로 GET하면 API 호출이 된다.
  - **첨부·블록 그림은 토큰 없는 별도 클라이언트**: https만(리다이렉트도, 10홉), 쿠키·Jar 없음, `Proxy: nil`(프록시면 IP 검사가 프록시를 본다), 타임아웃 10초. **Dialer `Control`에서 루프백·사설·링크로컬·지정 안 됨·멀티캐스트 IP를 막는다** — DNS를 푼 뒤 실제 연결 주소를 보므로 리다이렉트·DNS 리바인딩도 같이 막힌다.
  - 크기: 받는 양 10MB(파일 경로는 `GetFileContext`가 writer라 넘으면 오류 내는 limit writer, 외부는 `io.LimitReader`). `image.DecodeConfig`로 먼저 보고 **w×h > 4M px**면 버린다(압축 폭탄 — 디코드 메모리 ≤ 16MB). 썸네일 하나당 ctx 10초.
  - 봇 `image_url`을 직접 받으면 그 호스트가 내 IP·시각을 안다(Slack 앱은 프록시) — `GSLACK_IMAGES=off`로 끈다. README에 한 줄.

## 4. 바뀌는 곳

- `internal/cli/tuiimage.go`(새): `imageURLs`, 받기 Cmd(두 경로), 외부 클라이언트(IP 차단), 디코드·줄이기, `halfBlock(img, cols) []string`, 자리 크기 계산.
- `tuirender.go`: 파일·첨부·image 블록 자리에서 캐시 찾기. `blockSpans`는 span을 내므로 image 블록은 자리표시 span 대신 "그림 줄" 표시를 남기고 `prefixed` 뒤에 그림 줄로 바꾼다(구현 때 가장 작은 방법으로 — 블록을 그림 앞뒤로 나눠 그리는 것도 됨).
- `tui.go`: `m.imgs`, `imageMsg`, `showPreview`가 Cmd를 돌려줌, off 감지.
- `docs/TASKS.md`: T11의 "커스텀 이모지는 T7(그림)" → "커스텀 이모지는 `:name:` 그대로(emoji:read 없음)", T7b 줄(kitty·아바타·`v`).

## 테스트

- `TestHalfBlock`: 2×4px(위 두 줄 빨강, 아래 두 줄 파랑) → 2칸×2줄, 칸마다 `▀` 전경 빨강·배경 파랑(트루컬러). 투명 아래 → `▀` 배경 없음. 40칸 넘는 그림은 40칸으로, 비율 유지.
- `TestImageFetch`: 썸네일(fakeSlack)은 Authorization 있음, 외부 그림(TLS 테스트 서버, 테스트에서 클라이언트만 바꿈)은 **Authorization·Cookie 없음**. 첨부 `image_url`이 Slack 호스트여도 토큰 없음. 10MB 넘음·4M px 넘음·깨진 데이터 → 실패, 같은 URL 다시 안 받음.
- `TestImageDialBlock`: Control이 `127.0.0.1`·`10.0.0.1`·`169.254.169.254`·`[::1]`을 막고 공개 IP는 통과. http URL은 거절.
- `TestTUIPreviewImages`: previewMsg(파일 + 첨부) → Cmd가 URL마다 하나(같은 URL 둘이면 하나). 크기 아는 그림은 imageMsg 전후 줄 수 같음·고른 메시지 그대로, 자리표시가 반블록으로. 불러오는 중 화면에 imageMsg가 와도 안 덮음. off면 Cmd 없음.

## 안 하는 것(T7b 이후)

- kitty/Ghostty 선명 그림(유니코드 자리표시 — 그때 `clean`이 plane 16 사설 영역도 지울 것), 아바타·봇 아이콘, Quick Look `v`, context 블록 아이콘, 커스텀 이모지, 움직이는 GIF, 디스크 캐시, iTerm2·sixel.
