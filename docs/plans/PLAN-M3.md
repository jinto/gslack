# M3a 구현 계획 — watch, --notify

> 2026-10-13 · 대상: TASKS 3.2, 3.3(A 경로만) · 상태: 초안(리뷰 전)
> 미룸: 3.1의 RTM(B) — B 경로(0.3·0.5, R9 보안 확인)가 검증 전. 3.4 렌더링, 3.5 자동완성, 3.6 배포는 M3b.

## 근거(M0 0.4 실측, 2026-10-12~13 약 24시간)
- user 이벤트 `message.*`로 DM·채널 멘션·@channel·그룹 멘션·스레드 답글이 온다. ack 정상.
- slack-go `socketmode`가 끊김마다 스스로 재연결(지수 백오프 최대 5분, 밤새 16회). `invalid_auth`/`token_revoked` 등 치명 오류만 `RunContext`가 돌려준다 → **자체 재연결 루프는 만들지 않는다**.
- 가입하지 않은 공개 채널 이벤트도 온다(#random). 이벤트는 ts 순서가 아니다. 20분에 125개(대부분 봇).
- 내 edit/delete는 `message_changed/deleted`(user 비어 있음)로 와서 user 필터를 통과한다.
- `usergroups.list?include_users=true` 한 번으로 내 그룹 5개를 얻었다(재설치 후 scope OK).
- R8: Socket Mode 연결은 presence에 안 잡힌다(connections=0) — 앱 끄면 away. `--help`에 적는다.

## 명령
```
gslack watch [대상…] [--all] [--notify] [--json]
```
- 기본(대상 없음): **나와 관련된 것만** — DM(im), 그룹 DM(mpim), `<@나>`, `<!here>`, `<!channel>`, `<!everyone>`, 내 `<!subteam^S…>`, 내 메시지에 달린 스레드 답글.
- 대상을 주면: 그 대화의 모든 새 메시지 + 기본 알림(멘션·DM은 계속). 대상 해석은 `channelID`(읽기 명령처럼 부분 일치 허용).
- `--all`: 가입한 모든 대화의 모든 메시지.
- 항상 거름: 내 메시지, 가입하지 않은 대화, subtype 중 `message_changed`·`message_deleted`·`channel_join`·`channel_leave` 등 — **통과 목록**: `""`, `thread_broadcast`, `file_share`, `bot_message`, `me_message`.
- 출력: search와 같은 한 줄 `ts  MM/DD HH:MM  #채널  @이름: 미리보기`(DM은 `@상대`). 멘션이면 앞에 `*`. `--json`이면 NDJSON(`core.Message` 한 줄씩, 멘션 여부는 넣지 않는다 — 소비자가 text로 판단).
- Ctrl-C로 끝(exit 0). 치명 인증 오류는 exit 1.

## 핵심 결정
### 앱 토큰
- `session`에 `appToken` 필드. env 모드면 `GSLACK_APP_TOKEN`, 프로필이면 `sec.AppToken`. 없으면 usage 오류 "watch에는 앱 토큰(xapp-)이 필요하다 — `gslack auth login`에서 입력".
- `socketmode.New(slack.New(token, OptionAppLevelToken(app), …))`. 기존 `newAPI` 옵션에 app token만 추가하는 인자는 만들지 않고 watch에서 `slack.OptionAppLevelToken`을 붙인 클라이언트를 한 번 더 만든다(코드 3줄).

### 시작 시 한 번
1. `loadChannels`(가입 대화 — 캐시 쓰되 `--refresh` 존중). 이 목록에 없는 채널 이벤트는 버린다. 단 시작 후 새로 열린 DM(D…)은 목록에 없을 수 있다 → **im/mpim(channel_type)은 가입 검사 없이 통과**.
2. `usergroups.list(include_users)` → 내 그룹 ID 집합. `missing_scope`면 stderr 경고 한 줄 + 모든 subteam 멘션을 멘션으로 본다(놓치는 것보다 낫다).

### 이벤트 처리 — 순수 함수로 분리(테스트 대상)
```go
type watcher struct { me string; groups, members, targets map[string]bool; all bool; seen *dedupe; mine map[string]bool /*내가 쓴 스레드 ts*/ }
func (w *watcher) keep(id string, e *slackevents.MessageEvent) (show, mention bool)
```
- `event_id` dedupe: 최근 1000개 링(맵+슬라이스). 재연결 때 재전송 대비.
- 내 메시지: 버리되 `mine[thread_ts 또는 ts]=true` 기록 → 이후 그 스레드 답글을 알린다. 시작 전 스레드는 모른다(`parent_user_id`는 MessageEvent에 없음) — ponytail 한계로 적어 둔다.
- 순서: 받은 순서대로 출력(정렬 버퍼 없음). 늦게 온 이벤트는 ts가 앞일 뿐이다.
- 변환: `slackevents.MessageEvent` → `slack.Message{Msg: …}`로 옮겨 기존 `s.message(ctx, m, ch)` 재사용(이름 해석·fallbackText·permalink).

### --notify
- 멘션·DM일 때만(대상/`--all`로 보이는 일반 메시지는 안 띄움).
- `exec.Command("osascript", "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body)` — 본문을 스크립트에 끼워 넣지 않아 따옴표 주입이 없다. 실패는 stderr 경고(감시는 계속). darwin 아니면 시작 시 usage 오류.
- title `#채널 @이름`, body 미리보기 120자.

### 파일
`internal/cli/watch.go` 하나(명령 + watcher + notify). `root.go`에 등록, `session.go`에 appToken.

## 테스트 계획
- `keep` 표 테스트: 내 메시지 버림, 미가입 채널 버림, im 통과, `<@나>`/`<!here>`/내 그룹/남의 그룹, changed/deleted 버림, `--all`, 대상, 내 스레드 답글, 같은 event_id 두 번.
- 앱 토큰 없음 → exit 2, HTTP 0회(auth.test 전? — open이 auth.test를 부르므로 1회. 순서: open → appToken 검사).
- usergroups `missing_scope` → 경고 + subteam 멘션 통과.
- 소켓 연결 자체는 자동 테스트하지 않는다(spike로 24시간 실측). 라이브: 읽기 전용이므로 허락 없이 켜 둘 수 있다.

## 열린 질문(리뷰어에게)
1. 기본 필터에 mpim 전체를 넣는 것 — Slack 앱 알림 기본과 같다. 맞나?
2. 시작 전 스레드의 답글을 못 잡는 한계 — 답글 이벤트마다 `conversations.replies`로 부모를 보는 건 호출 폭증. 허용 가능한가?
3. NDJSON에 `mention` 필드를 넣을까(core.Message 계약 밖)?
4. 더 뺄 것?

## R. 리뷰 반영(Codex·Fable, 2026-10-13) — 본문과 충돌하면 이 절이 우선

- **ack 먼저**: 이벤트를 받자마자 `sm.Ack`, 그다음 필터·이름 해석·출력(`users.info` 지연이 소켓 읽기를 막아 재전송되는 것 방지). 소비 루프는 `ctx.Done()`도 본다.
- **Ctrl-C**: `RunContext`가 `context.Canceled`를 돌려준다 → nil(exit 0). 그 밖의 반환(invalid_auth·not_authed·account_inactive·token_revoked·404)은 exit 1. 재연결 래퍼는 만들지 않는다.
- **내 스레드 답글**: `e.Message.ParentUserId == me`(slackevents가 `Message`를 항상 채운다). `mine` 맵·"시작 전 스레드 모름" 한계 삭제. 남는 한계: 내가 *답글만 단* 스레드는 안 잡는다 — help에.
- **변환**: `s.message(ctx, slack.Message{Msg: *e.Message}, e.Channel)` 한 줄. subtype 검사는 바깥 `e.SubType`.
- **멘션 판정**은 `cmp.Or(e.Text, fallbackText(m))`(blocks만 있는 봇 메시지). 토큰 단위 정규식: `<@U…(|…)?>`, `<!here…>`, `<!channel…>`, `<!everyone…>`, `<!subteam^S…(|…)?>`.
- **가입 검사**: 시작 때 `loadChannels(ctx, true)`(24시간 프로세스라 캐시 안 씀). `<@나>` 직접 멘션·im·mpim은 가입 검사 없이 통과. 브로드캐스트·그룹 멘션·대상·`--all`만 가입 대화로 제한. 실행 중 새로 가입한 채널은 재시작 필요 — help에.
- **그룹**: `usergroups.list` 결과 중 `Users`에 내가 있는 것만.
- **dedupe**: `event_id` 맵, 1000개 넘으면 `clear` — 3줄.
- **NDJSON**: 전용 `json.Encoder`(들여쓰기 없음). `mention` 필드는 넣지 않는다(Codex; Fable은 추가 권고 — TUI가 필요해지면 그때).
- **클라이언트**: `newAPI(..., opts ...slack.Option)` 가변 인자 → `slack.OptionAppLevelToken`. 앱 토큰 검사는 open 뒤(auth.test 1회는 허용).
- **stderr 상태줄**: 시작 `watching @나 — DM·멘션·내 스레드`, 재연결 오류(`ConnectionError` 시도·대기), 재연결 성공. 끊긴 동안의 메시지는 다시 오지 않는다 → help에 "빈틈은 `gslack unread`". presence(R8)도 help에.
- **출력**: 멘션 표시(`*`) 삭제 — 기본 모드는 이미 관련된 것만이다. DM 라벨은 채널 캐시에 없으면(새 DM) `@보낸 사람`.
- PRD의 `--mentions-only`는 기본값으로 흡수, 반대 방향 `--all`로 바꾼다 — PRD·TASKS 문구 갱신.
