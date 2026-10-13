# M3c c.2 구현 계획 — `unread`: session 프로필은 `client.counts` 한 번

> 2026-10-13 · 대상: TASKS M3c c.2 · 상태: 초안(리뷰 전)
> 목표: session(xoxc) 프로필의 `unread`를 대화마다 info+history(DM 29초, `--all` 약 4분) 대신 `client.counts` 한 번(0.4초)으로 거른다. 비공식 API라 실패하면 A 방식으로 폴백(R6).

## 흐름

1. `a.channels(ctx, types)`로 대상 대화 목록(기존 그대로 — `--type`/`--all` 의미 유지).
2. `s.prof.Kind == "session"`이면 `client.counts` 한 번 → `has_unreads`인 대화 ID 집합.
   - 대상 목록을 그 집합으로 **줄인 뒤** 기존 `scanUnread`(info+history)를 그대로 돌린다. 안 읽은 대화는 보통 몇 개라 몇 번 호출로 끝난다.
   - 출력(개수·`more`·최신 메시지·정렬·JSON 형식)은 A와 **같다** — 개수는 여전히 history로 센다(본인·입장 제외 규칙 동일).
3. `client.counts`가 실패하면(HTTP·`ok:false`·디코드 오류) stderr에 한 줄 경고 후 A 방식(전체 스캔) — R6 격리.
4. user(xoxp) 프로필은 변화 없음.

## 범위 차이(264개 중 82~105개만 옴)
- spike 0.8: `channels`는 최근 활동 대화만 온다. 응답에 없는 대화는 **읽음으로 간주**한다(GUI 사이드바와 같은 근거 — Slack 클라이언트도 이 응답으로 굵게 표시).
- 검증(라이브, 사용자가): 같은 워크스페이스 xoxp 프로필 `unread --all --json`과 session 프로필 결과를 비교. 차이가 있으면 기록하고 TASKS에 남긴다. 숨은 비교 플래그는 만들지 않는다(프로필 두 개로 충분).

## 구현
- `client.counts`는 slack-go에 없다 → 직접 POST(form `token`), spike `counts()`를 가볍게 옮긴다. 응답은 `channels`/`mpims`/`ims`의 `id`, `has_unreads`만 디코드(멘션 수·threads는 YAGNI — 출력에 없다).
- HTTP 클라이언트: `newAPI`가 만드는 `http.Client`(쿠키 transport·30초)를 재사용해야 쿠키가 붙는다. `newAPI`에서 `newHTTP(cookie, timeout)`을 떼어 session에 `httpc`와 `token`을 둔다(필드 2개).
- URL: 테스트는 `apiURL`, 실제는 `prof.URL + "api/client.counts"`(spike가 검증한 팀 도메인).
- 파일: `internal/cli/unread.go`(counts 함수 + 분기), `session.go`(필드·`newHTTP`).

## 테스트
- 가짜 서버에 `client.counts` 추가: 대화 하나만 `has_unreads:true`. session 프로필(env `GSLACK_TOKEN=xoxc-…`)로 `unread --all --json` → 그 대화만 `conversations.info` 호출, 결과 같음.
- 폴백: `client.counts`가 `ok:false` → 경고 + 전체 스캔 결과(기존 TestUnread 결과와 동일).
- 쿠키가 counts 요청에 붙는지: 가짜 서버가 http라 `cookieTransport`(https Slack만)는 안 붙인다 — 테스트 안 함(기존 경로 재사용이라 c.1/0.3 라이브로 커버).

## 안 하는 것
- `mention_count`·threads 표시, `channels --unread`에 적용 — 요청 오면.
- 개수를 counts만으로 내기(history 생략) — counts에 개수가 없다(`has_unreads` 불리언).

## Codex·Fable 리뷰 반영(2026-10-13)
- **"출력은 A와 같다" 정정**: 행 집합이 다를 수 있다 — counts 응답에 없는 대화(오래 조용한 채널)와 뮤트 채널은 빠진다. Long 도움말에 session 단서를 단다. 미포함 대화를 A로 추가 스캔하면 속도 이득이 사라지므로 안 한다 — 라이브 비교로 확인(비공개 채널 ID가 응답에 있는지 포함).
- **취소는 폴백하지 않는다**: `NewRequestWithContext`, `ctx.Err() != nil`이면 그대로 반환. HTTP 상태 코드 검사(200 아니면 오류), body close.
- 폴백 경고에 오류 문자열을 넣는다(왜 느린지 보이게).
- URL: `apiURL`이 있으면 그것, 아니면 `strings.TrimRight(prof.URL, "/") + "/api/"`.
- `newHTTP`로 `api`와 같은 설정(쿠키·30초·리다이렉트)의 클라이언트를 따로 만든다(호스트가 달라 공유 이득 없음). 토큰을 실으므로 https `*.slack.com`이 아니면 거절(→ 폴백).
- 테스트: counts 성공(POST·form token, 표시된 대화만 info 호출), `ok:false`·HTTP 500 폴백. `fakeSlack`에 토글 1개.
- 안 함: counts의 `last_read`로 info 생략(필드 미검증 — 라이브 덤프로 확인되면 다음에), 캐시 이후 생긴 새 DM 감지(A도 같은 구멍), 쿠키 단위 테스트(기존 `cookieTransport` 재사용).
