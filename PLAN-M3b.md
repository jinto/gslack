# M3b 구현 계획 — 렌더링 마무리, 셸 자동완성, README·배포

> 2026-10-13 · 대상: TASKS 3.4, 3.5, 3.6 · 상태: 초안(리뷰 전)
> 미룸: 3.1의 RTM(B), 0.3·0.5·0.8 — B 경로는 R9(보안 정책) 확인 전. Homebrew tap — git remote가 없다(아래 3.6).

## 근거(2026-10-13 실측: 6개 채널 `read -n 100`, 2613줄을 패턴으로만 훑음)
PRD §5 체크리스트 대부분은 M1에서 이미 됐다(`Render` 표 테스트 11건, `fallbackText`, `withTags`). 남은 실제 결함:

| # | 관찰 | 원인 |
|---|---|---|
| G1 | 본문 첫 줄이 비어 보이는 메시지 10건(`@acme deploy: ` 뒤 다음 줄부터) | `text`가 `\n`으로 시작. 더 나쁜 건 `preview()`가 첫 `\n`에서 잘라 **search·watch·unread 미리보기가 통째로 빈다** |
| G2 | `@BAC59U2F8` 80건 | `bot_message`에 `bot_id`만 있고 `username`·`bot_profile`이 없다 |
| G3 | `@subteam` 13건 | `<!subteam^S…>`에 라벨이 없는 형태가 실제로 온다 |
| — | `*굵게*` 718줄, `:x:` 등 이모지 코드 | 읽는 데 지장 없음 → **안 한다**(아래) |

## 3.4 렌더링

### G1 공백 정리
- `printMessages`: 렌더 결과를 `strings.TrimSpace` 후 줄 나눔.
- `preview()`: `TrimSpace` 후 첫 줄. 호출부(search·watch·unread·send 결과) 전부 이 하나로 고쳐진다.

### G2·G3 이름 해석을 하나로
지금 `Render(text, user, channel)`는 호출부 4곳이 같은 두 클로저를 넘긴다. Slack ID는 접두사로 종류가 갈린다(U/W 사용자, C/G/D 대화, S 사용자 그룹, B 봇) →
```go
func Render(text string, name func(id string) string) string
func (s *session) name(ctx, id string) string // 접두사로 userName / channelName / groupName 분기
```
- `<!subteam^S…>`(라벨 없음) → `name("S…")` → `@handle`, 모르면 지금처럼 `@subteam`.
- 그룹 이름: `usergroups.list` 한 번(watch가 이미 부른다 — `myGroups`를 `loadGroups`로 바꿔 전체 목록을 세션에 두고, watch는 거기서 내 그룹을 고른다). 디스크 캐시 `usergroups.json`(TTL 24h, `--refresh`). 라벨 없는 subteam이 **나왔을 때만** 부른다(lazy). `missing_scope`면 조용히 `@subteam`.
- 봇 이름: `message()`에서 `bot_id`만 있으면 `bots.info`(scope `users:read`) — 세션 메모리 맵으로 같은 봇은 1회. 디스크 캐시는 안 한다(채널당 봇 몇 개). 실패하면 지금처럼 bot_id.

### 안 하는 것
- `*bold*`·`_it_`·`` `code` ``를 ANSI로: TTY 판정·`NO_COLOR`·파이프 처리가 붙는데 원문도 읽힌다. TUI(M4)에서.
- `:emoji:` → 유니코드: 1800개 표가 필요(새 의존성). rich_text의 `unicode` 필드만 쓰면 같은 화면에서 `:x:`와 ❌가 섞인다.
- subtype 표시(`channel_join` 등): 본문이 이미 "님이 참여했습니다"라 충분.

## 3.5 셸 자동완성
- `completion` 명령은 cobra가 자동으로 준다(bash/zsh/fish/powershell). 추가 코드 없음 — README에 설치법.
- 동적 완성 `completeTargets` 하나를 다음에 단다:
  - 첫 인자: `read send mark upload thread react edit delete download`, 모든 인자: `watch`
  - `whois`(사용자만), `search --in`(채널), `--from`(사용자), `auth use/logout`(프로필 이름 — config에서)
- 후보: 캐시의 `#채널`(public/private), `@handle`(삭제 안 된 사용자, 설명 탭에 표시명). im/mpim은 뺀다(`@상대`로 간다).
- **네트워크 0회**: 완성은 매 탭마다 프로세스가 뜬다 → `open()`(auth.test)을 부르지 않고 프로필 이름 → `store.OpenCache`로 파일만 읽는다. TTL이 지난 캐시도 쓴다(`LoadCache`에 만료 무시 변형 하나). 캐시 없으면 후보 없음 + `ShellCompDirectiveNoFileComp`. env 모드(`GSLACK_TOKEN`)는 캐시 경로에 team/user가 필요해서 완성 안 함.
- 접두사 거르기는 우리가 `strings.HasPrefix`로(셸마다 다르다). 사용자는 handle 외에 표시명 접두사도 맞춘다(`@재<TAB>` → `@jaekwon`).
- 알려진 한계: bash는 `#`이 주석이라 `'#gen<TAB>`처럼 따옴표가 필요 — README에.

## 3.6 README·배포
- `README.md`: 설치(`go install`), Slack 앱 만들기(`slack-app-manifest.yaml` 붙여 넣기, **Distribute 끄기**), `auth login`(xoxp + xapp), 명령 표, 자동완성 설치, watch 한계(presence·빈틈은 `unread`), B(xoxc)는 **미검증·비공식**이라 사용 전 회사 보안 정책 확인(R9) — xoxc 추출법은 B spike 후에 쓴다.
- 버전: `root.Version` + `var version = "dev"`(main, `-ldflags -X`). `gslack --version`.
- `.goreleaser.yaml`: darwin/linux × amd64/arm64, `CGO_ENABLED=0`, ldflags 버전. `goreleaser check`는 설치돼 있지 않아 `go run github.com/goreleaser/goreleaser/v2@latest check`로 한 번 검증(의존성에 안 들어감).
- **Homebrew tap은 안 만든다**: 사용자 결정(2026-10-13, "상관없다") → TASKS 3.6에서 뺀다.

## 테스트 계획
- `Render` 표: 라벨 없는 subteam → 그룹 이름 / 모르면 `@subteam`; 기존 11건은 resolver 하나로 바꿔 유지.
- `preview("\n\n*x*\n…")` → `*x*`.
- 가짜 서버: `bots.info` → 이름, 같은 봇 두 메시지에 호출 1회; `usergroups.list` → `@handle`, `missing_scope` → `@subteam`.
- 완성: 캐시 파일만 있는 임시 HOME에서 `__complete read '#ge'` → `#general`, `__complete whois @재` → `@jaekwon`, HTTP 0회, 만료 캐시도 후보.
- `gslack --version`.
- 라이브: 같은 6개 채널 다시 읽어 G1~G3 건수 0 확인(읽기만).

## 열린 질문(리뷰어에게)
1. `Render`를 resolver 하나로 합치는 것 — ID 접두사 분기가 깨지는 경우(Enterprise Grid W…, Slack Connect)?
2. 완성에서 만료 캐시를 쓰는 것, env 모드 완성 포기 — 괜찮나?
3. ANSI 굵게·이모지 변환을 안 하는 것 — 3.4 "고도화"로 충분한가?
4. 더 뺄 것?

## R. 리뷰 반영(Codex·Fable, 2026-10-13) — 본문과 충돌하면 이 절이 우선

- **채널 후보는 맨 이름**(`general`): cobra 스크립트가 세 셸 모두 `eval`로 `__complete`를 불러 `#…`는 주석으로 사라진다(Fable 실측). `ParseTarget`이 맨 단어를 채널로 받는다. README의 따옴표 안내 삭제.
- **표시명 접두사 매칭 삭제**: 셸이 입력 접두사로 다시 거른다. 후보는 `@handle`, 표시명은 설명 칸에만.
- **완성 세부**: `GSLACK_TOKEN`이면 명시적으로 후보 없음. `--profile` 반영. `len(args)`로 위치 제한(cobra는 완성 때 Args를 안 본다). `upload` 둘째 인자는 파일 완성. 플래그는 `RegisterFlagCompletionFunc`(`search --in/--from`, `--profile`). `LoadCache(c, name, maxAge)` 인자 하나로(0이면 만료 무시).
- **G1**: `TrimSpace` 대신 앞뒤 **빈 줄만** 버린다(첫 줄 들여쓰기·코드 보존). `preview`는 첫 비어 있지 않은 줄.
- **G3**: Resolver 단일화 대신 `Render(text, user, channel, group)` + `s.render(ctx, text)` 헬퍼로 호출부 중복 제거(태그 문법이 이미 종류를 안다). 그룹은 **세션 메모리만**(디스크 캐시·`--refresh` 없음): `loadGroups`가 `usergroups.list(include_users)` 1회, watch의 `myGroups`도 같은 결과를 쓴다(프로세스당 1회라 갱신 문제 없음). 실패하면 `@subteam`. `richText`가 `usergroup`·`broadcast` 요소를 `<!subteam^S…>`·`<!here>`로 되살린다.
- **G2**: `bots.info` 실패(`SlackErrorResponse`)도 메모 — 실패한 봇 두 메시지에 호출 1회. search 경로는 `SearchMessage`에 bot_id가 없어 대상 밖.
- **버전**: `internal/cli`에 `var Version = "dev"`, ldflags `-X github.com/jinto/gslack/internal/cli.Version={{.Version}}`.
- **goreleaser**: `version: 2`, `-trimpath`, `release.disable: true`(remote 없음), `dist/`를 `.gitignore`. 검증은 `build --snapshot --clean` 1회.
- 테스트 추가: `printMessages`의 `"\n본문"`·들여쓴 코드, rich_text 그룹 멘션, 완성 HTTP 0회(`fakeSlack.reqs`), upload 둘째 인자 directive.
