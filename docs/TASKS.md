# gslack — TASKS

PRD.md의 마일스톤 순서를 따른다. 각 항목은 완료 기준(✓)을 만족해야 체크한다.

## M0. Spike — 가정 검증(코드는 버려도 됨)

**A 경로(M0 게이트)**
- [x] 0.1 `slack-app-manifest.yaml` 작성: user scopes(PRD §3, `*:write` 포함), Socket Mode, app-level scope `connections:write`, user 이벤트 `message.im message.mpim message.channels message.groups`(`app_mention`은 봇 이벤트라 제외 — 멘션은 `<@U…>` 매칭으로 판별). 테스트 워크스페이스에 설치. **Distribute App은 절대 켜지 않는다**
  ✓ xoxp, xapp 발급
  → 2026-10-12: Acme에 앱 `gslack`(A0XXXXXXXXX) 생성, 관리자 승인 없이 바로 설치, xoxp 발급. xapp은 0.4 때 발급
- [x] 0.2 xoxp: `auth.test`, `users.conversations`, `conversations.history`. 버스트 테스트: `conversations.history` limit=200을 1분 안에 16회 이상 호출
  ✓ 429 없이 매번 200건 페이지가 옴(internal 앱이 Tier 3 유지)
  → 2026-10-12: auth.test OK, users.conversations 264개, burst 16회/7.5s 모두 200건, 429 없음. PASS
- [x] 0.4 실시간 A: Socket Mode로 다른 사람이 보낸 DM/멘션 이벤트 수신(R3). 두 번째 계정 필요
  ✓ envelope ACK, 재접속, 자기 메시지 필터 동작. 연결 중 `users.getPresence`로 active/away 확인(R8)
  → 2026-10-12 부분 완료: xapp 발급, Socket Mode 연결 OK, DM(`message.im`) 이벤트 수신·ack OK(event_id 포함).
  → 2026-10-12 오후(4시간 연속): **채널 멘션 이벤트 수신 OK**(17:04 #dev `<@나>`), **자기 메시지 필터 OK**, 그룹 멘션 `<!subteam^…>` 수신(소속 판정은 usergroups:read 필요). 내 edit/delete는 `message_changed`/`message_deleted`(user 비어 있음)로 와서 자기 필터를 통과한다 — M3에서 subtype으로 거를 것. **재연결 OK**: 연결 약 5시간 뒤(20:45) Slack이 끊자 slack-go socketmode가 스스로 다시 붙었다(connecting → connected, 이벤트 계속). R8 관찰: 재연결 직후 presence `away, auto_away=true` — Socket Mode 연결은 사용자를 active로 만들지 않는다(앱 연결은 프레즌스에 영향 없음). 데스크톱 앱을 끈 상태 비교는 미확인
  → 2026-10-13(24시간 연속): **완료.** 재연결 16회 모두 자동, 프로세스 재시작 0. R8: 데스크톱 앱 연결이 끊긴 07:05 이후 Socket Mode만 연결된 상태에서 presence `away, connections=0` — Socket Mode는 프레즌스에 안 잡힌다. 앱 재설치(`usergroups:read`, 토큰 그대로) 후 `usergroups.list?include_users=true`로 내 그룹 5개 확인 → 그룹 멘션 판정 가능
    실행: `cd docs/spike && GSLACK_TOKEN=$(cat ~/.config/gslack/spike-xoxp) GSLACK_APP_TOKEN=$(cat ~/.config/gslack/spike-xapp) go run . watch`
- [x] 0.6 안 읽은 수(A): 채널에 `conversations.info`의 `last_read`가 오는지, DM의 `unread_count` 확인(R2)
  → 2026-10-12: public/group/mpim/im 모두 `last_read`가 온다. `unread_count`는 전부 0 — 비공개 채널 1곳은 last_read 이후 메시지가 1건 있는데도 0이었다. 따라서 `unread_count`는 쓰지 않고, 대화마다 `conversations.history(oldest=last_read)`로 센다(대화당 info+history 2회 호출 → `unread`는 최근 활동 대화로 범위를 줄이거나 느린 명령으로 둔다). DM의 `unread_count` 동작은 안 읽은 DM이 없어서 미확인
- [x] 0.7 결과를 PRD §3·§7에 반영하고 명령별 인증 방식·scope 매트릭스를 확정
  → 2026-10-13 M3c c.4에서 완료

**B 경로(독립 spike, M0을 막지 않음)**
- [x] 0.3 xoxc+d: 쿠키 RoundTripper로 0.2와 같은 호출 + 파일 업로드 1건(R7)
  ✓ acme 워크스페이스에서 성공(보안 정책 확인 후, R9)
  → 2026-10-13: 사용자 진행 결정. 조회 OK — auth.test, users.conversations 264개, `gslack auth status`(session)·`read` 그대로 동작. **업로드(R7) OK**(허락 후 셀프 DM에 1건, `gslack upload` 그대로 — 2단계 다른 호스트에서도 쿠키 동작) + `download`로 받아 내용 일치. 토큰은 브라우저 DevTools에서 손으로(xoxc: `localStorage.localConfig_v2`, d: 쿠키) — 로그아웃하면 다시
    실행: `cd docs/spike && GSLACK_TOKEN=$(cat ~/.config/gslack/spike-xoxc) GSLACK_COOKIE=$(cat ~/.config/gslack/spike-xoxd) go run . auth|counts|rtm`
- [x] 0.5 실시간 B: `RTMOptionDialer`(쿠키 Jar, Domain `.slack.com`)로 RTM 연결, 메시지 수신(R4)
  → 2026-10-13: 연결 OK(앱·xapp 없이). 약 10분에 DM(폰에서 보낸 내 메시지)·채널 메시지 30건·reaction_added·im_marked·file_change 수신. **Socket Mode와 달리** 가입한 모든 채널 메시지가 다 오고(필터 필수), user_typing이 대부분(140건). R8: RTM 연결은 presence `active, connections=2` — 접속 중으로 보인다. slack-go `unmarshalling_error` 6건(모르는 이벤트 타입, 무시 가능)
- [x] 0.8 안 읽은 수(B): `client.counts` 응답 확인
  → 2026-10-13: 한 번에 0.4초 — channels 82·mpims 1·ims 22개의 `has_unreads`·`mention_count`, threads 요약. A(29초/`--all` 약 4분) 대비. 주의: channels는 264개 중 82개만 온다(최근 활동 대화로 보임)

## M1. 핵심 — 얇은 수직 슬라이스
> 2026-10-12 M1a(읽기 전용: 1.1~1.9, 1.13) 완료 — PLAN-M1a.md. `read --mark`, profile+method limiter는 M1b로. 1.5의 limiter 미구현(429는 OptionRetryConfig로 처리)
> 2026-10-12 M1b(1.10~1.12, `read --mark`) 완료 — PLAN-M1b.md. limiter는 만들지 않기로(unread만 waitRateLimit). unread 기본 im,mpim(29초), `--all` 약 4분. 라이브: self-DM send·mark 확인
- [x] 1.1 `go mod init`, cobra 루트, 전역 플래그(`--profile --json --raw --refresh`), 종료 코드 규칙, 오류 JSON
- [x] 1.2 `internal/core` 정규화 모델(Message/Channel/User) — 모든 명령과 watch의 `--json` 출력 타입
  ✓ JSON 직렬화 테스트
- [x] 1.3 자격 증명 저장소: macOS Keychain(`security`, 값은 `-w` 인자 대신 stdin), 그 외 OS는 0600 파일
  ✓ 평문 설정 파일·셸 히스토리·`ps`에 토큰이 없음
- [x] 1.4 `auth login/status/list/use/logout` + 프로필 설정(`~/.config/gslack/config.json`). 토큰은 프롬프트/stdin/`GSLACK_TOKEN`, 쿠키는 raw `Cookie` 헤더
- [x] 1.5 클라이언트 팩토리: 프로필 → slack.Client(xoxp 또는 xoxc+쿠키, `OptionRetry`, profile+method limiter, context 취소)
- [x] 1.6 이름 캐시(users/channels via `users.conversations`, TTL 24h, 미스 시 `users.info` 폴백) + target 파서(`#ch`, `@user`, ID, permalink — enterprise/Slack Connect 도메인 포함)
  ✓ 파서 테이블 테스트
- [x] 1.7 기본 포맷터: 사용자 이름 해석, 멘션/링크, 상대 시간, ts 항상 표시
- [x] 1.8 `channels`
- [x] 1.9 `read`(`-n --since --before --mark`, 페이지네이션)
- [x] 1.10 `send`(인자/stdin, `--thread`, DM은 `conversations.open`)
- [x] 1.11 `thread`(permalink 또는 target+ts)
- [x] 1.12 `unread`(A는 근사치, B는 `client.counts`), `mark`(확인 + `--yes`)
- [x] 1.13 `users`, `whois`
- [x] 1.14 `@이름` 부분 일치: 정확 일치(handle→표시명→실명)가 없으면 세 필드 부분 일치, 여러 명이면 후보 나열(`@민수` → minsu)

## M2. 확장
> 2026-10-12 M2b(2.3, `send --at`) 완료 — PLAN-M2b.md. upload는 대상 먼저·stdin 없음. 파일 전송은 전체 타임아웃 없는 별도 클라이언트, 리다이렉트는 Slack으로만. 라이브(self-DM): download·upload·send --at 확인(예약은 지정 시각에 게시됨)
> 2026-10-12 M2a(2.1, 2.2, 2.4, 2.5의 edit/delete) 완료 — PLAN-M2a.md. M2b: 2.3 upload/download, `send --at`. 라이브(self-DM): react·edit·delete 확인 — user token으로 보낸 내 메시지에도 앱 bot_id가 붙는다(본인 판정은 user로만). status set은 미확인
- [x] 2.1 `search`(`--in --from`을 쿼리 문법으로 변환, legacy 차이 문서화)
- [x] 2.2 `react` / `--remove`
- [x] 2.3 `upload`(`UploadFile`, external flow), `download`(`files.info` → `url_private_download`, Slack 호스트에만 자격 증명)
  ✓ 파일명 충돌 시 덮어쓰지 않음, 부분 다운로드가 남지 않음
- [x] 2.4 `status set/clear`(presence는 뺌 — 수동 away는 실제 접속 상태를 보장하지 않고, 조회는 `whois`)
- [x] 2.5 `edit`, `delete`(확인 + `--yes`), `send --at`(RFC3339/`2h`, 미래 검사만, 목록·취소는 Slack 앱)

## M3. 실시간·마무리
- [x] 3.1 이벤트 소스 하나로 Socket Mode(A)와 RTM(B)을 같은 `core.Message` 스트림으로 정규화(3초 ack, `event_id` dedupe, 자기 메시지 필터)
  → M3c c.3에서 구현(RTM 메시지를 `slackevents.MessageEvent`로 바꿔 같은 `keep`·출력)
- [x] 3.2 `watch`(필터: target, `--mentions-only`(here/channel/subteam 포함), 참여 스레드 답글, changed/deleted 제외, NDJSON, 지수 백오프 재연결)
  → 2026-10-12 watch 스파이크 관찰: (1) user event `message.channels`가 **가입하지 않은 공개 채널**(#random)까지 온다 — 가입 대화로 거를 것. (2) 이벤트가 ts 순서가 아니고, 켜기 전 메시지가 늦게 오기도 한다 — event_id dedupe, 출력은 ts 기준. (3) `<!subteam^S…>` 그룹 멘션을 판정하려면 `usergroups:read`(내가 속한 그룹 목록)가 필요 — manifest에 추가함, 앱 재설치 필요. 20분에 이벤트 125개(대부분 봇 알림) — 기본 필터 필요
- [x] 3.3 `--notify`: `osascript` 알림(멘션/DM)
  → 2026-10-13 M3a(3.2·3.3, A 경로) 완료 — PLAN-M3.md. `--mentions-only`는 기본값으로, 반대는 `--all`. 재연결은 slack-go에 맡김. 라이브: 기본·`--all --json` 스트림 확인, `--notify` 알림 표시는 미확인. 3.1의 RTM(B)은 B spike 후
> 2026-10-13 M3b(3.4~3.6) 완료 — PLAN-M3b.md. 라이브(6개 채널 2559줄): 빈 첫 줄·봇 ID·`@subteam` 0건. ANSI 굵게·이모지 변환은 안 함(TUI에서). Homebrew tap은 안 만듦(사용자 결정)
- [x] 3.4 렌더링 고도화(PRD §5 mrkdwn 체크리스트)
  ✓ 포맷터 테스트
- [x] 3.5 셸 자동완성(cobra completion + 캐시 기반 채널/사용자 이름) — 채널은 `#` 없는 맨 이름(eval에서 주석), 네트워크 0회
- [x] 3.6 README(앱 설치법, 두 인증 방식, B는 비공식 표기), goreleaser(`release.disable` — remote 없음). xoxc 추출법은 B spike 후

## M3c. B 경로 활용(다음 작업 — 이 순서로)
> 2026-10-13 B spike(0.3·0.5·0.8) 통과 후 사용자와 정한 순서. 각 항목은 work pipeline(PLAN → 리뷰 → 구현 → 리뷰 → ponytail)으로
- [x] c.1 `gslack auth import`: Slack 데스크톱 앱 로컬 저장소에서 xoxc(LevelDB `localConfig_v2`) + `d` 쿠키(Cookies sqlite, Keychain "Slack Safe Storage"로 복호화)를 읽어 프로필 생성 — DevTools 손 작업 제거. 비공식이라 Slack 업데이트로 깨질 수 있음을 help에
  → 2026-10-13 완료 — PLAN-M3c-c1.md. 복호화·파싱은 플랫폼 중립(stdlib `crypto/pbkdf2`, CI 테스트), Keychain·경로·leveldb·sqlite는 `_darwin`. 의존성은 goleveldb 1개(sqlite는 시스템 `sqlite3`, pbkdf2는 stdlib). 팀 여러 개면 `[team]`으로 고름(후보 나열).
    **버그 2개 발견·수정**(`Keychain.Set` 루트): (1) `security -w` 빈 값이 tty의 getpass를 읽어 stdin 무시 → `setsid`로 tty 떼어 해결. (2) getpass 128B 한계로 session 비밀값(xoxc+쿠키) 잘림 → macOS는 짧은 xoxp만 Keychain, **120B 넘으면 0600 파일 폴백**(`macStore`가 길이로 라우팅, 전환 시 반대쪽 정리). at-rest 암호화를 session에 한해 완화(사용자 결정 2026-10-13, FileVault가 커버). login에도 적용되는 수정.
    라이브: `--profile acme-session auth import acme` → `auth status` kind=session → `read '@박제권'` 확인. 실행은 사용자(분류기가 자격 증명 접근을 막아 Claude는 코드만)
- [x] c.2 `unread`: session(xoxc) 프로필이면 `client.counts` 한 번(0.4초). 264개 중 105개만 오는 범위 차이 확인, 실패 시 A 방식 폴백(R6)
  → 2026-10-13 구현 — PLAN-M3c-c2.md. counts의 `has_unreads`로 대상을 줄인 뒤 기존 info+history로 센다(출력 형식 동일). 응답에 없는 대화는 읽음으로 본다. 실패(HTTP·ok:false·디코드)는 경고 후 A, Ctrl-C는 폴백 안 함. 토큰은 https `*.slack.com`으로만. 라이브 비교(xoxp 11 vs session 9): 채널 9개(비공개 2 포함)·개수 모두 일치. xoxp에만 있는 2개는 둘 다 최신 메시지가 Slackbot `huddle_thread`인 DM — Slack은 안 읽음으로 안 친다(session 쪽이 GUI와 같다). A에서 `huddle_thread`도 거르면 같아진다(미적용)
- [x] c.3 (3.1) `watch`에 RTM 소스: xapp 없이 session으로. 모든 채널 메시지가 오므로 기존 `keep` 필터 재사용, 연결 중 presence active
  → 2026-10-13 구현 — PLAN-M3c-c3.md. session 프로필이면 RTM(xapp 있어도), 아니면 Socket Mode. RTM 메시지를 `slackevents.MessageEvent`로 바꿔 `keep`·출력(`emit`)을 그대로 쓴다. channel_type은 가입 대화 캐시(+`D` 접두)로 채움. 쿠키 Jar `.slack.com`(spike 그대로), 쿠키 없으면 usage 오류. `InvalidAuthEvent`·ok:false 거절은 종료(slack-go는 인증 오류 외엔 영원히 재시도 — Socket Mode에도 같이 적용). 테스트: 가짜 ws 서버 e2e(필터 5종), 인증 실패·거절·쿠키 없음.
    → 2026-10-14 라이브(사용자, 약 하루): RTM 연결·DM 수신·멘션(스레드 안 ↳ 포함)·`@channel` OK, 재연결 4회 모두 자동. **미확인**: 멘션 없는 내 스레드 답글(`parent_user_id`) — 그룹 멘션과 구분되는 줄이 아직 없음, 데스크톱 앱 끈 상태의 presence
    개선 후보(요청 오면): 봇 DM 거르기(`--no-bots` — 캘린더·리포트 봇 DM이 많다), DM 줄의 이름 중복(대화 이름 = 보낸 사람이라 두 번 찍힌다). `@channel`/`@here`를 멘션으로 치는 건 설계대로
- [x] c.4 (0.7) PRD §3·§7에 결과 반영, 명령별 인증·scope 매트릭스
  → 2026-10-13 완료(문서 작업이라 계획 리뷰는 생략하고 Codex·Fable 사실 확인 한 번). §3 A/B 표를 실측으로, 매트릭스는 공통 scope(`*:read users:read`) + 명령별 추가 scope + session 라이브 여부, §7 R1~R9 상태. 틀린 줄 정리(`channels --unread`, `presence`, `--type`, unread "정확"). manifest에서 안 쓰는 `reactions:read emoji:read users:write` 제거(설치된 앱은 그대로 둬도 됨).
    session으로 미확인: mark·send·edit/delete·search·react·status·그룹 멘션 — 쓰기 명령은 셀프 DM으로 사용자가

## M4. TUI(별도 기획)
- [x] M1~M3 코어를 재사용하는 TUI PRD 작성
  → 2026-10-13 PRD-TUI.md. 사용자 결정: 받은편지함형, 실시간은 프로필 따라 자동, `gslack tui`, 읽음은 `m`·답장 때만. 리뷰 반영: 한 줄 = 대화, 잠금 없음(API는 Cmd, 이름 해석은 Update), 실시간은 T3으로(핵심 루프 먼저), 한 패널 모드·도움말·`--all` 뺌
- [x] T1 보기: 시작 채우기 + 목록 + Enter 미리보기 + j/k/R/q, 상태줄 writer, `unreadRows`·`replies` 추출
  → 2026-10-13 구현 — PLAN-M4-T1.md. 첫 채우기는 Cmd(세션 캐시는 그 Cmd만 채움), 미리보기는 Enter seq로 늦은 응답 버림, 상태줄은 app 복사본의 errw(원래 errw는 안 바꿈), TUI 전용 취소. 이름·상태줄은 Inline으로 한 줄. 받은편지함 (N) 머리줄·상대시간은 뺌.
    **라이브 확인 필요(사용자)**: 실제 터미널에서 `gslack tui` — 한글 폭, 그룹 DM 이름, R, 미리보기 스크롤
- [x] T2 처리: `r` 답장(스레드 규칙), `m` 치우기, 답장 뒤 자동 읽음, `o`
  → 2026-10-13 구현 — PLAN-M4-T2.md. 답장 대상은 미리보기와 같은 `thread()` 술어(답글 달린 부모는 스레드 아님), 입력 중 키는 목록보다 먼저, 보내는 중엔 ctrl+c 말고 무시. 자동 읽음은 응답의 channel·ts로. 실패 분류: Slack 거절(ok:false·429·4xx)은 입력 유지, 그 밖(타임아웃·5xx)은 "불확실" + 미리보기 다시. `m`은 미리보기로 본 최신까지(`max`), "이미 더 뒤까지 읽음"은 성공. done은 그 ts까지 읽은 줄만. `o`는 macOS `open`, 다른 OS는 상태줄에 URL. 의존성: textinput이 `atotto/clipboard`(indirect)를 끌어옴.
    **라이브 확인 필요(사용자, 셀프 DM)**: `r` 보내기·자동 읽음, 스레드 답장 뒤 답글 ts로 `conversations.mark`가 받아지는지, `m`, `o`
- [x] T3 실시간: watch 소스 콜백 인자화, 병합(ts 가드), 재연결 자동 R, `--notify`
  → 2026-10-14 구현 — PLAN-M4-T3.md. watch를 `newWatcher`·`liveDialer`·`listen`(메시지·재연결 콜백)으로 나눠 TUI와 같이 쓴다. 첫 채우기가 소스를 훑기 전에 띄운다(채우는 동안 온 것도). `keep`은 소스 고루틴에서(PRD §4와 다름 — 세션을 안 건드려서, RTM 전체 메시지가 Update를 거치지 않게), alert만 push. 병합은 ts 가드, 치운 줄에 새 것이면 1부터, 채우는 중 온 것은 끝난 뒤 다시 적용(알림 한 번). R은 채우기가 못 보는 줄(history에 없는 스레드 답글, 채우기 대상 밖 — 채널 멘션·실행 중 새 DM)과 늦게 반영된 mark를 둔다. 채우는 중 재연결이면 끝나고 한 번 더. 스레드 줄은 `m`·답장 뒤 읽음 모두 TUI 안에서만, 채널 줄 답장은 그 메시지까지만 mark. 앱 토큰 없는 user 프로필은 "실시간 꺼짐"으로 연다.
    동작 변경(사용자 승인): watch도 그룹·사용자 목록 실패는 경고(사용자 목록 실패면 users.info 폴백).
    **라이브 확인 필요(사용자)**: session(RTM)·user(Socket Mode)에서 DM·멘션·스레드 답글이 줄로 들어오는지, 재연결 뒤 자동 R, `--notify`
- [ ] T4 채널 구역: 멘션 없는 가입 채널의 안 읽은 글을 "채널" 구역(흐리게)으로, 꼭 읽을 것은 "나에게" 구역 — 두 단계 채우기, 뮤트 제외(session만), user는 뒤에서 4분
