# M3c c.3 구현 계획 — `watch`: session 프로필은 RTM(앱 토큰 없이)

> 2026-10-13 · 대상: TASKS M3c c.3(= 3.1의 B) · 상태: 초안(리뷰 전)
> 목표: session(xoxc+d) 프로필은 xapp 없이 `watch`가 된다. 필터·출력·`--notify`·`--json`은 Socket Mode와 같다.

## 소스 선택
- `s.prof.Kind == "session"` → RTM. 그 밖(xoxp) → 지금처럼 Socket Mode, xapp 없으면 같은 usage 오류.
- session에 xapp이 있어도 RTM(앱 설치 불필요가 B의 요점, 연결 중 presence active). 규칙은 종류 하나로만.

## 정규화 — `keep`과 출력은 그대로
- RTM `*slack.MessageEvent`를 `*slackevents.MessageEvent`로 바꿔 기존 처리에 넣는다(새 타입 없음):
  `Channel, User, Text, SubType, Message: &ev.Msg, Root: ev.Root`.
- RTM 메시지에는 `channel_type`이 없다 → 가입 대화 캐시(`s.chans`의 Kind)로 `im`/`mpim`을 채운다. 캐시에 없고 ID가 `D`로 시작하면 `im`(실행 중 새로 열린 DM).
- 기존 루프에서 "메시지 하나 출력·알림" 부분을 `a.emit(ctx, s, w, e, notify)`로 떼어 두 루프가 같이 쓴다.
- dedupe: RTM에는 event_id·재전송이 없다 → 안 함. ack 없음.

## RTM 연결
- `s.api.NewRTM(slack.RTMOptionDialer(d))`, `d`는 `websocket.DefaultDialer` 복사 + 쿠키 Jar(`.slack.com`, spike 0.5 그대로). 쿠키 파싱은 `http.ParseCookie`(stdlib). session에 `cookie` 필드 1개 추가(`open`에서 채움).
- `rtm.connect` 자체는 `s.api`(쿠키 transport·30초)로 가므로 그대로.
- 이벤트: `ConnectedEvent`(count>0이면 기존 "다시 연결됨" 문구), `ConnectionErrorEvent`(기존 문구), `InvalidAuthEvent` → 오류 반환("세션이 만료됐다 — gslack auth import"), `MessageEvent` → 정규화 → emit. 나머지(typing·presence·unmarshalling_error 등)는 무시.
- Ctrl-C: `go r.Disconnect()` 후 nil 반환(`Disconnect`는 무버퍼 채널이라 막힐 수 있다 — 기다리지 않는다).
- 재연결은 slack-go `ManageConnection`의 지수 백오프에 맡긴다(spike에서 확인).

## 도움말
- Long: "Socket Mode(앱 토큰 xapp-) 또는 session 프로필이면 RTM(앱 토큰 불필요, 비공식)". presence 줄: "Socket Mode는 presence를 active로 만들지 않는다, RTM은 연결 중 active로 보인다".
- 오류 문구: xapp 없음 → "…또는 session 프로필(gslack auth import)".

## 테스트
- e2e: 가짜 서버에 `rtm.connect`(→ `ws://…/rtm`) + gorilla `Upgrader`로 `hello` 후 메시지 이벤트 3개(DM 1, 내 메시지 1, 미가입 채널 일반 1)를 보낸다. `GSLACK_TOKEN=xoxc-…`, `--json`, context 취소로 끝. DM 한 줄만 나오는지, `channel_type`이 im으로 채워졌는지.
- `TestWatchNeedsAppToken`은 xoxp 그대로 통과.

## 안 하는 것
- 끊긴 동안 빈틈 메우기(A와 같음 — unread로), 실행 중 새 가입 채널(A와 같음).
- RTM의 다른 이벤트(reaction·typing·im_marked) 출력 — 요청 오면.
- session + xapp에서 Socket Mode를 고르는 플래그 — 요청 오면.

## Codex·Fable·advisor 리뷰 반영(2026-10-13)
- **쿠키**: `http.ParseCookie`는 Domain이 없어 host-only가 된다(wss-primary.slack.com에 안 감) → spike의 `;`/`=` 분리 + `Domain .slack.com, Path /` 그대로. 쿠키가 0개면 바로 오류(조용한 재연결 루프 방지).
- **루프**: `IncomingEvents`는 닫히지 않는다 → `select { ctx.Done / IncomingEvents }`. `InvalidAuthEvent`(invalid_auth·revoked·404)는 slack-go가 그 뒤 조용히 끝나므로 반드시 오류 반환. 종료 코드는 Web API invalid_auth와 같게.
- **Ctrl-C**: `rtm.connect`·dial은 context 없이(Background 10초) 돈다 — CLI는 프로세스가 끝나므로 받아들인다. `go r.Disconnect()`로 재연결만 멈춘다(기다리지 않음).
- **테스트**: `Upgrader{CheckOrigin: true}`(slack-go가 Origin api.slack.com을 보냄). `Execute`를 고루틴으로, out은 `io.Pipe`, 줄 수만큼 읽고 cancel. ws 핸들러는 보낸 뒤 read에서 막혀 있게(닫으면 재연결·재전송). 이벤트 순서 [내 메시지, 미가입 채널 일반, message_changed, 내 스레드 답글(parent_user_id), DM] → 줄 2개(답글, DM)면 앞은 걸러진 것. 멘션 없는 DM이 통과 = channel_type im 채움의 증거. 쿠키 Jar는 localhost에 안 가므로 쿠키 검증은 라이브로.
- **라이브 확인 목록**(사용자): RTM 스레드 답글에 `parent_user_id`, broadcast에 `root`가 오는지(spike는 미확인), 데스크톱 앱을 끈 상태에서 presence. 도움말은 "관찰됨"으로만.
- 한계에 추가: 실행 중 새로 생긴 그룹 DM(캐시에 없음, D 접두도 아님), RTM은 미가입 채널 멘션을 주지 않는다.
- kind 맵은 member 맵과 함께 한 번 만든다.
