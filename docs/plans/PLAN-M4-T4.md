# M4 T4 구현 계획 — `gslack tui` 채널 구역

> 2026-10-15 · 대상: TASKS M4 T4(새 항목), PRD-TUI.md §1·§2·§3 · 상태: Codex·Fable 리뷰 반영
> 목표: 멘션이 없어도 가입한 채널의 안 읽은 글을 TUI에서 본다. 꼭 읽을 것(나에게)과 구분되게.
> 사용자 결정(2026-10-14): **한 목록, 두 구역**(위 "나에게", 아래 "채널" 흐리게) · 채널 구역은 **안 읽음, 뮤트 제외** · user(xoxp) 프로필도 **뒤에서 채널 전부 훑기**(약 4분).
> PRD-TUI §1 비목표의 "`--all`(모든 채널 메시지)"을 이 범위로 좁혀 되살린다 — 모든 메시지가 아니라 "안 읽은 채널 한 줄씩".

## 0. 사실 확인(spike `mute`, 2026-10-14)

- session: `users.prefs.get` ok. 뮤트는 `prefs.all_notifications_prefs`(JSON 문자열)의 `channels.<ID>.muted`에만 온다(19개). 옛 `muted_channels` 키는 없다.
- `client.counts`는 뮤트 채널도 준다(13개 중 9개 `has_unreads=true`, 모두 `mention=0`) → 우리가 거른다.
- xoxp: `users.prefs.get` → `missing_scope`(공개 scope로 받을 수 없는 비공개 메서드), `client.counts` → `not_allowed_token_type`. → **user 프로필은 뮤트를 거르지 않는다**(부르지도 않는다 — help에 한 줄).
- Socket Mode 일반 채널 메시지·뮤트·스레드 답글 전달은 0.4·3.2 관찰뿐 — 라이브에서 다시 본다.

## 1. 줄 모델 — `forMe`

- `tuiRow.forMe bool`: "나에게" 구역. DM·그룹 DM은 늘 `true`. 채널은 안 읽은 메시지 중 `keep`의 alert(내 멘션·@here/@channel·내 그룹·내 스레드 답글)가 있으면 `true`.
- **forMe 채널 줄의 `latest` = 가장 새 alert 메시지**. `r`이 T2 규칙대로 그 멘션에 스레드로 답하고, `o`가 그 멘션을 연다. `count`는 그 채널의 안 읽은 수 전체.
- 정렬: `forMe` 먼저, 그 안에서 `latest.TS` 내림차순(`setRows`).
- **ts 가드는 `latest.TS` 하나로 둔다**(Codex 2·4 검토): forMe 줄은 비alert를 무시하므로(§4) latest가 최신이 아니어도 가드가 틀리지 않는다 — alert끼리만 비교된다. 비forMe 줄은 latest가 곧 최신. 대가는 forMe 줄 count가 실행 중 비alert만큼 덜 세는 것(다음 채우기가 바로잡음). *ponytail: 정확히 세려면 `seenTS` 필드 하나.*

## 2. 채우기 — 두 단계

- `fill(channels bool)`: 1단계(DM·그룹 DM — 지금 그대로)를 돈다. `filledMsg`에 `scope`(이번 단계의 **client.counts로 줄이기 전** 대상 전체 — 줄인 뒤면 Slack 앱에서 읽은 채널 줄이 안 지워진다)와 `next = channels`를 싣는다.
- Update `filledMsg`: `merge(items, scope)` → `pending` 다시 적용·비움 → `next`면 2단계 Cmd(`fillChannels`)를 내고 **`filling`은 `true` 그대로**(그 사이 온 이벤트도 `pending`). 마지막 단계에서만 `filling=false`, 그때 `refill`이면 `fill(false)`.
- 2단계: 가입 public·private 채널 전부(`s.chans`에서 Cmd가 매번 고른다 — 첫 채우기 뒤 `s.chans`는 읽기만). session은 `unreadRows` 안의 `client.counts`로 줄이고, user는 전부(약 4분). 결과에서 `muted && !forMe`인 항목은 버린다(뮤트 채널의 멘션은 "나에게"로 남는다).
- 1단계가 실패해도 `next`면 2단계는 돈다(ctx 취소면 둘 다 금방 실패).
- **누가 어느 단계를**: 첫 채우기·`R` = `fill(true)`, 재연결 자동 R = `fill(false)`(되찾을 것은 끊긴 사이 DM — 채널을 매번 4분 훑지 않게). 끊긴 사이의 채널 글은 `R`로.
- 채우는 중 `R` → 상태줄 "채우는 중 — 끝나면 R"(지금은 조용히 무시).
- **`merge(items, scope)`**: 지금의 `m.chans` 자리를 `scope`로. 1단계가 채널 줄을, 2단계가 DM 줄을 지우지 않는다. 남기는 스레드 줄과 채우기 줄이 겹치면 지금은 `latest.TS`가 새 쪽 — 여기에 **옛 줄이 forMe이고 채우기 줄이 아니면 옛 줄**(내 스레드 답글 줄이 더 새 비alert 채널 글 때문에 "채널" 구역으로 떨어지지 않게, Fable 2).
- `m.chans`는 "첫 채우기 끝남" 표시로만 남는다(`newWatcher`·소스를 한 번만). 첫 채우기가 만든 `w`(watcher)·`muted`도 `filledMsg`에 실어 모델이 한 번 받는다 — 다시 채우기는 `newWatcher`를 다시 부르지 않는다(T3 잠금 없음 불변식).

### 진행 표시(Fable 1)

- `scanUnread`의 `progress := isTTY(a.errw)`는 statusWriter에서 `false`라 지금 TUI에는 진행이 안 나간다. → `a.errw`가 `statusWriter`여도 켠다. 끝의 `\r\033[K`는 statusWriter엔 `\r`만(`TrimSpace`가 `\033[K`를 못 뗀다 → 빈 상태).
- View: 채우는 중에도 `m.status`가 있으면 그것을("확인 중 12/240"), 없으면 "채우는 중…".

### forMe 판정(채우기)

- `unreadRows(ctx, s, chans, w *watcher)` — `unread` 명령은 `nil`. `unreadOne`이 history 메시지마다 `w != nil`이면 `keep(&MessageEvent{Channel, ChannelType: c.Kind, User, Text, SubType, Message: &m.Msg, Root: m.Root})`(Root — broadcast의 "내 스레드" 판정, Fable 4) → alert면 `forMe`, 첫(가장 새) alert를 `latest`로. `unreadItem.forMe`.
- `keep`은 `w`만 읽는다 → `scanUnread` 고루틴에서 불러도 된다.
- history에는 스레드 답글이 없다 → **켜기 전 스레드 멘션은 여전히 못 본다**(PRD §2 빈틈). `mention_count`로 올리는 건 latest를 그 멘션으로 못 잡아서(`r`이 엉뚱한 데로) 안 한다.

## 3. 뮤트

- `s.mutedChannels(ctx) (map[string]bool, error)`: `users.prefs.get`을 `clientCounts`처럼 팀 주소로 POST(`isSlackURL` 검사 그대로), `all_notifications_prefs`를 풀어 `muted=true`인 채널. **session 프로필만** 부른다. 실패(HTTP·ok:false·깨진 JSON)는 경고 + `nil`(거르지 않음).
- 첫 채우기 Cmd에서 `newWatcher` 뒤, 소스를 띄우기 전에 한 번 → 소스 클로저·2단계 결과 거르기가 읽기만(잠금 없음, T3 §2와 같은 이유). 실행 중 뮤트를 바꾸면 재시작까지 반영 안 됨.
- 술어 하나 `muted[ch] && !alert → 버림`을 채우기 결과(§2)와 실시간(§4) 두 곳에.

## 4. 실시간

- TUI watcher는 `newWatcher` 직후 **`w.all = true`** → `keep`이 가입 채널의 모든 메시지에 `show`. 소스 콜백:
  - `!show` → 버림.
  - `!alert`이고 (뮤트이거나 나를 부르지 않은 스레드 답글 — `ThreadTS != TS && SubType != "thread_broadcast"`) → 버림. Slack 사이드바도 남의 스레드 답글로 채널을 굵게 하지 않는다.
  - 나머지 `push(liveMsg{e, alert})`. `--notify`는 `alert`만(지금 그대로).
- `apply(e, alert, notify)`:
  - 줄 없음 → 새 줄 `count=1`, `forMe=alert`.
  - **forMe이고 안 치운 줄에 비alert → 무시**(latest를 멘션으로 둔다).
  - 그 밖(ts 가드 통과): 지금처럼 `count+1`(치운 줄이면 1부터), `latest` 교체, `forMe = alert || (forMe && !done)`.
- 부하: 이제 가입 채널의 모든 메시지가 Update(`s.message`·`render`)를 거친다(T3는 alert만). *ponytail: 체감되면 이름 해석을 Cmd로.*

## 5. 화면

```
 나에게 (2)
 ● @minsu                    1  10/14 14:02  @minsu: 배포 끝났어요
 ● #dev ↳                    3  10/14 13:58  @jk: <@나> 확인 부탁
 채널 (3)
 ○ #todo_ai_pics_by_user    12  10/14 13:40  @sora: 새 이미지 올렸어요
```

- 머리줄 둘(`나에게 (n)`, `채널 (n)` — n은 안 치운 줄 수). "나에게 (0)"도 보인다(꼭 읽을 게 없다는 정보). 채널 줄이 없으면 `채널` 머리줄은 생략. 줄이 하나도 없으면 지금 문구 "나에게 온 것이 없다".
- 채널 줄: ● 대신 ○, 줄 전체 `Faint`. 치운 줄은 지금처럼 빈 점 + Faint.
- View는 머리줄 포함 표시 줄 목록을 만들고, 커서의 표시 줄(= `cur` + 앞선 머리줄 수)이 보이게 창을 자른다. 머리줄에는 커서가 가지 않는다(`j/k`는 행 기준 그대로).
- 키·답장·치우기 규칙 변화 없음. 채널 구역 줄에 `r`이면 최신 메시지에 스레드로(T2 규칙).
- 알아둘 동작: forMe 채널 줄의 `m`은 그 멘션(또는 미리보기 최신)까지만 읽음 → 그 뒤 글은 다음 `R`에 ○ 줄로 다시 뜬다. Enter 미리보기(최근 20개)에 오래된 멘션이 안 보일 수 있다 — `o`가 그 멘션을 연다.
- tui Short: "받은편지함 화면 — 나에게 온 것(DM·멘션)과 안 읽은 채널을 보고, 답하고(r), 치운다(m)". Long에 user 프로필은 뮤트를 거르지 못함·채널 훑기 약 4분.

## 테스트

- `TestUnreadForMe`(fakeSlack history): 채널 안 읽음 중 `<@나>`가 가운데 → `forMe`, `latest`가 그 멘션, `count` 전체. 멘션 없음 → `!forMe`, latest 최신. 내 스레드 broadcast(Root) → forMe. `w == nil`(unread 명령) → 지금과 같은 결과.
- `TestTUIChannels`(모델):
  - `fill(true)`의 1단계 `filledMsg{next}` → 2단계 Cmd, `filling` 유지, pending 다시 적용. 2단계 결과가 DM 줄을 안 지움, 반대도. 1단계 실패여도 2단계.
  - `reconnectedMsg` → `fill(false)`(next=false). 채우는 중이면 마지막 단계 뒤 `fill(false)`. 채우는 중 `R` → 상태줄 문구.
  - scope는 counts로 줄이기 전 목록: Slack에서 읽은 채널 줄이 R 뒤 사라짐.
  - merge: forMe 스레드 줄 vs 더 새 비alert 채우기 줄 → forMe 줄 남음.
  - 정렬: forMe 채널 줄이 비forMe보다 위.
  - `liveMsg` 비alert 채널 → 채널 구역 새 줄. 같은 채널 alert → forMe로 올라감. forMe 줄에 비alert → 그대로. 치운 forMe 줄에 비alert → 채널 구역 `count=1`.
  - 소스 콜백: 뮤트 비alert 버림, 뮤트 alert 통과, 남의 스레드 답글 버림, broadcast 통과. 채우기 결과의 뮤트 비forMe 버림.
  - View: 머리줄 둘, "나에게 (0)", 작은 `listH`에서 채널 구역 커서가 보임. 채우는 중 상태줄에 진행, `\033[K` 안 남음.
- `TestMutedChannels`: prefs JSON(`all_notifications_prefs` 문자열) → map, 깨진 JSON·ok:false는 오류.
- `TestTUIRTM` 바꿈: `w.all`이라 멘션 없는 채널 메시지도 비alert `liveMsg`로 온다.
- 기존 unread·watch 테스트 통과. 완료 기준 `go test -race ./...`. 라이브(사용자): session·user 각각 채널 구역, 뮤트 채널이 빠지는지(session), Socket Mode로 일반 채널 글이 들어오는지.

## 안 하는 것

- 채널 줄을 메시지 단위로 펼치기, 채널 본문 답장 키, 구역 접기·건너뛰기 키.
- 켜기 전 스레드 멘션(`mention_count`), 뮤트 변경 실시간 반영, forMe 줄의 정확한 count(`seenTS`).
- `unread` 명령의 뮤트 거르기(요청 오면 같은 `mutedChannels`로).
