# M2b 구현 계획 — upload, download, send --at

> 2026-10-12 · 대상: TASKS 2.3, 2.5의 `send --at` · 상태: 초안(리뷰 전)

## 범위

| 명령 | API | scope(manifest에 있음) |
|---|---|---|
| `upload <대상> <파일 \| -> [--thread ts] [--title] [--comment] [--name]` | `files.getUploadURLExternal` → 업로드 → `files.completeUploadExternal` (slack-go `UploadFileContext`) | `files:write` |
| `download <파일ID \| 메시지> [-o 디렉터리]` | `files.info` 또는 메시지의 files → `url_private_download` (slack-go `GetFileContext`) | `files:read` |
| `send <대상> <본문> --at <시각>` | `chat.scheduleMessage` | `chat:write` |

**안전**: upload·`send --at`은 회사 Slack에 쓴다 — 라이브는 허락 후 self-DM에만. download는 읽기(디스크에만 쓴다).

## 핵심 결정

### 공통: 보내는 대상 해석
`send`의 대상 해석(정확 일치만, permalink → 그 스레드 답글, DM 없으면 `conversations.open`)을 `a.sendTarget(ctx, s, t) (ch, thread string, err)`로 빼서 send·upload가 공유한다. `--thread`와 permalink 충돌 규칙도 그대로.

### 파일 전송 타임아웃
지금 HTTP 클라이언트는 요청 전체 30초 제한이라 큰 파일 업로드/다운로드가 끊긴다. 세션이 토큰·쿠키를 들고 있다가 파일 전송용 클라이언트를 `Timeout: 0`(Ctrl-C/context로만 끊김)으로 따로 만든다(`s.fileAPI()`). API 호출(getUploadURL·complete·files.info)도 그 클라이언트로 가지만 짧은 호출이라 문제없다.

### 2.3a upload
- 인자 순서는 **대상 먼저**(`send`/`read`와 같게). PRD의 `upload <file> <target>`은 바꾼다.
- 파일: 경로면 `os.Stat` 크기 + `File` 경로(slack-go가 연다). `-`면 stdin을 메모리로 읽어 `Reader`+크기, 이름은 `--name`(기본 `stdin.txt`). 크기 0이면 usage(Slack이 거절).
- `--title`(기본 파일 이름), `--comment`(initial_comment, mrkdwn 그대로), `--thread ts`.
- 완료는 비동기(메시지 ts를 안 준다) — 출력은 `올렸다 <이름> (F…) → <대상>`, `--json {id, title, channel}`.

### 2.3b download
- 인자: 파일 ID(`F…`) 하나, 또는 메시지(`msgRef` — permalink / 대상 ts)면 그 메시지의 파일 전부.
- `files.info`/메시지에서 `url_private_download`. 외부 파일(`is_external`, 구글 드라이브 등)이나 URL이 없으면 건너뛰고 경고(링크만 보여준다). 자격 증명은 slack-go가 Slack HTTPS 호스트에만 붙인다(`ensureURLMayReceiveToken`), 쿠키 transport도 Slack 호스트만.
- 디스크 쓰기:
  - 이름은 `filepath.Base`로 정리(`../` 제거), 비거나 `.`/`..`이면 `F….bin`.
  - `os.CreateTemp(dir, ".gslack-*")`에 받은 뒤 `os.Link(tmp, 최종)`으로 **덮어쓰지 않고** 확정, 실패(EEXIST)면 `이름 (1).ext`, `(2)` … 그 다음 tmp 삭제. 중간에 끊기면 tmp만 지운다(defer) — 부분 파일이 남지 않는다.
  - `-o`는 기존 디렉터리만(없으면 usage). 기본 `.`.
- 출력: 저장 경로 한 줄씩. `--json [{id, name, path, size}]`.

### 2.5 send --at
- `--at`: 상대 시간(`2h`, `30m`, `1d`, 앞의 `+` 허용) 또는 RFC3339(오프셋 포함). `parseWhen(…, +1)` 재사용. `--tz`는 만들지 않는다 — RFC3339에 오프셋이 있다.
- 검증(usage): 지금+1분 이후, 120일 이내(Slack 한도). "채널당 5분 30개"는 Slack 오류(`restricted_too_many`)를 그대로.
- `chat.scheduleMessage`(thread 가능). 출력 `예약했다 01/02 15:04 → <대상> (Q…)`, `--json {channel, scheduled_message_id, post_at}`.
- 예약 목록·취소 명령은 만들지 않는다(Slack 앱의 "예약됨"에서 본다/취소한다) — `--help`에 적는다.

## 테스트 계획
가짜 서버에 `files.getUploadURLExternal`, 업로드 URL(가짜 서버 경로), `files.completeUploadExternal`, `files.info`, 파일 다운로드 경로, `chat.scheduleMessage` 추가.
- upload: 경로/stdin, 크기 0 거절, `--thread`, DM open, 부분 일치 거절(send와 같은 헬퍼).
- download: 파일 ID, 메시지의 여러 파일, 이름 충돌 → `(1)`, `../evil` → Base, 외부 파일 건너뜀, 다운로드 실패 시 tmp 없음, `-o` 없는 디렉터리.
- send --at: `2h` → post_at ≈ now+2h, 과거·121일 → usage(호출 0), thread.
- 라이브: download 읽기 자유(내 테스트 업로드 후). upload·send --at은 허락 후 self-DM.

## 열린 질문(리뷰어에게)
1. upload 인자 순서를 PRD와 달리 대상 먼저로 바꾸는 것?
2. 파일 전송용 무제한 타임아웃 클라이언트 — 다른 방법(예: idle timeout)이 나은가?
3. download가 메시지 permalink를 받아 파일 전부 받는 것 — 파일 permalink(`/files/U…/F…/name`)도 받아야 하나?
4. 예약 목록/취소 없이 `send --at`만 — 괜찮나?
5. 더 뺄 것?

## R. 리뷰 반영(Codex·Fable, 2026-10-12) — 본문과 충돌하면 이 절이 우선

- **클라이언트 하나**(fileAPI 삭제): `http.Client{Timeout: 0}` + 기본 Transport 복제에 `ResponseHeaderTimeout: 30s`(요청 본문 전송 뒤 응답 헤더까지만 — 업로드·다운로드 본문은 안 끊고, API 호출은 헤더까지 보호). Ctrl-C는 context. 연결·TLS 타임아웃은 기본 Transport 값 유지.
- **리다이렉트마다 호스트 재검증**(Codex): slack-go는 첫 다운로드 URL만 검사하고 Authorization을 붙인 채 리다이렉트를 따른다 → `CheckRedirect`에서 https Slack 호스트(또는 테스트 API 호스트)가 아니면 거절.
- **upload는 Reader 하나로**: 직접 `os.Open`+`Stat`(디렉터리·0바이트 → usage, API 0회) → `Reader`+`Filename`+`FileSize`. 경로 업로드는 slack-go가 `Filename`을 무시해서 `--name`과 어긋난다. **stdin 업로드는 뺀다**(메모리 상한 문제, Codex 권고) — `--name`도 같이 삭제. `--title` 기본값 계산 삭제(Slack이 파일 이름을 쓴다).
- **대상 해석**: `(s *session) sendChannel(ctx, t) (string, error)` = 정확 일치 사용자 → channelID → noDM이면 open. thread 도출(permalink/`--thread`)은 open 전에 각 명령 안에서.
- **send --at**: `post_at`은 Unix 초 문자열(slack-go는 그대로 넘긴다 — Codex). 검증은 미래인지만(`--until`과 같게); 1분·120일 검사 삭제(Slack 오류 그대로). `+2h`는 parseWhen이 이미 받는다. **예약 목록·취소는 만들지 않는다**(Fable; Codex는 취소 권고 — `Q…` id를 출력하고 help에 "취소는 Slack 앱의 예약됨"으로 둔다. 필요해지면 `DeleteScheduledMessageContext`).
- **download**:
  - `F…`는 msgRef 전에 검사(`channelIDRe`가 `F`를 몰라 채널 이름으로 해석되던 것).
  - 메시지는 원본 `slack.Message.Files`의 ID → 각각 `files.info`(URL·외부 여부). 외부/`hidden_by_limit`/URL 없음은 경고 + `File.Permalink`를 보여주고 건너뜀.
  - 쓰기: `O_CREATE|O_EXCL`로 이름 선점(`report (1).pdf` — 확장자 앞), 실패하면 그 파일 삭제. tmp+Link 대신(더 짧고 하드링크 의존 없음). `fs.ErrExist`만 다음 번호로.
  - 종료 코드: 1개 이상 저장 → 0(건너뜀은 stderr 경고), 0개 → 1, 메시지에 파일 없음 → 2. `--json size`는 실제 쓴 바이트.
  - 파일 permalink(`/files/…`)는 받지 않는다(`download <대상> <ts>`가 read 흐름).
- 출력 방향은 send와 같게 `← <대상>`. 파일: `files.go`(upload/download), `--at`은 sendCmd 안.
