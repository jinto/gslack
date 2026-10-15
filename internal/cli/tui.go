package cli

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/spf13/cobra"
)

func (a *app) tuiCmd() *cobra.Command {
	var notify bool
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "받은편지함 화면 — 나에게 온 것(DM·멘션)과 안 읽은 채널을 보고, 답하고(r), 치운다(m)",
		Long: `위 "나에게"는 DM·그룹 DM·멘션·내 스레드 답글, 아래 "채널"(흐리게)은 멘션 없이 안 읽은 가입 채널이다.
DM을 먼저 채우고 채널은 뒤에서 채운다 — user 프로필은 채널마다 물어서 몇 분 걸린다.
뮤트 채널은 session 프로필에서만 뺀다(user 토큰으로는 뮤트를 알 수 없다). 뮤트여도 멘션은 "나에게"로 온다.
켜기 전에 온 스레드 답글은 보이지 않는다(실행 중 온 것만).`,
		Args: cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, _ []string) error {
			if a.asJSON {
				return usagef("tui는 --json을 쓰지 않는다")
			}
			if notify && runtime.GOOS != "darwin" {
				return usagef("--notify는 macOS에서만")
			}
			if !isTTY(a.out) {
				return usagef("tui는 터미널에서만 연다")
			}
			parent := cmd.Context()
			s, err := a.open(parent)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithCancel(parent) // q로 끝나면 돌던 Cmd도 끝낸다
			defer cancel()
			// 경고·진행 출력은 상태줄로. a.errw는 그대로 둔다 — RunE 오류는 원래 stderr로 가야 하고, 늦게 끝난 Cmd가 읽어도 경합이 없다.
			ta := *a
			m := newTUI(ctx, &ta, s)
			if m.liveErr != nil && s.prof.Kind == "session" { // 쿠키 없음 — 앱 토큰 없는 user 프로필만 실시간 없이 연다
				return m.liveErr
			}
			m.notify = notify
			p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithOutput(a.out))
			ta.errw = statusWriter(p.Send)
			m.push = p.Send
			_, err = p.Run()
			switch {
			case errors.Is(err, tea.ErrInterrupted):
				return context.Canceled
			case errors.Is(err, tea.ErrProgramKilled) && parent.Err() != nil:
				return parent.Err()
			}
			return err
		}),
	}
	cmd.Flags().BoolVar(&notify, "notify", false, "새로 온 것이면 macOS 알림")
	return cmd
}

// statusWriter는 a.errw 대신 상태줄로 보낸다. Send는 Update가 끝나길 기다리므로 Update 안에서는 a.errw에 쓰지 않는다.
type statusWriter func(tea.Msg)

func (w statusWriter) Write(p []byte) (int, error) {
	w(statusMsg(p))
	return len(p), nil
}

type (
	statusMsg string
	filledMsg struct {
		scope []core.Channel // 이번에 훑은 대상(client.counts로 줄이기 전) — merge가 이 대화들만 채우기를 따른다
		items []*unreadItem
		err   error
		next  bool            // 이어서 채널을 채운다
		w     *watcher        // 첫 채우기만 — 실패해도 싣는다(다음 R이 newWatcher·소스를 다시 타지 않게)
		muted map[string]bool // 첫 채우기만
	}
	previewMsg struct {
		seq    int
		head   string
		ch     string
		msgs   []slack.Message
		err    error
		latest string // history 미리보기의 최신 ts — 스레드 미리보기는 ""(답글 ts로 대화를 mark하면 사이의 안 본 메시지까지 읽음이 된다)
		read   string // history 미리보기의 last_read — 안 본 첫 메시지를 맨 위에. 모르면 ""(맨 아래)
	}
	sentMsg struct {
		row          tuiRow
		ts           string // 치운 데까지 — DM 본문이면 보낸 메시지, 스레드·채널 줄이면 그 줄의 latest(보내는 사이 온 것은 남긴다)
		err, markErr error
	}
	markedMsg struct {
		ch, ts string
		err    error
	}
	liveMsg struct {
		e     *slackevents.MessageEvent
		alert bool // 나에게 온 것. 아니면 가입 채널의 일반 글(남의 스레드 답글·뮤트 채널은 소스에서 거른다)
	}
	reconnectedMsg struct{}
	liveDownMsg    struct{ err error } // 실시간 소스가 끝났다
)

type tuiRow struct {
	ch         core.Channel
	name, text string // Update에서 해석해 둔다 — View는 세션을 건드리지 않는다
	count      int
	more, done bool         // done: 치움(m 또는 답장 뒤 읽음)
	forMe      bool         // "나에게" 구역 — 아니면 "채널"
	latest     core.Message // forMe 채널 줄이면 가장 새 "나에게 온 것"(r·o가 그리로)
}

// 스레드 답글이면 미리보기는 그 스레드다(history에는 답글이 없어 T1에선 thread_broadcast만).
func (r tuiRow) thread() bool { return r.latest.ThreadTS != "" && r.latest.ThreadTS != r.latest.TS }

// replyThread는 답장을 달 스레드 ts다. 답글이면 그 스레드, 채널이면 그 메시지에 스레드로(멘션에 채널 본문으로 답하지 않게), DM·그룹 DM이면 대화에 바로.
func replyThread(r tuiRow) string {
	switch {
	case r.thread():
		return r.latest.ThreadTS
	case r.ch.Kind == core.KindIM || r.ch.Kind == core.KindMPIM:
		return ""
	}
	return r.latest.TS
}

// tuiModel: Cmd(고루틴)는 Slack API만 부르고, 이름 해석·렌더링은 Update(한 고루틴)에서 한다.
// 세션 캐시는 첫 채우기 Cmd만 채우고, Update는 첫 filledMsg 전엔 세션을 건드리지 않는다.
type tuiModel struct {
	ctx      context.Context
	a        *app
	s        *session
	w        *watcher        // 첫 채우기 전엔 nil
	muted    map[string]bool // 뮤트 채널(session만) — 첫 채우기 뒤 읽기만
	rows     []tuiRow
	cur      int
	filling  bool
	seq      int                     // Enter마다 늘린다 — 늦게 온 미리보기를 버린다
	pv       struct{ ch, ts string } // 마지막 history 미리보기의 최신 ts — m이 본 데까지 치운다
	listH    int
	vp       viewport.Model
	ti       textinput.Model
	reply    *tuiRow // 답장 입력 중 — 값 복사라 입력 중 filledMsg가 줄을 바꿔도 대상은 그대로
	sending  bool
	status   string
	progress string // 채우기 진행("\r확인 중 12/240") — status를 덮지 않게 따로

	dialer  *websocket.Dialer
	liveErr error         // 실시간이 꺼진 이유 — nil이면 켜짐
	push    func(tea.Msg) // 소스 고루틴 → Update(p.Send)
	notify  bool          // --notify
	pending []liveMsg     // 채우는 중 온 것 — 단계마다 다시 적용(ts 가드로 한 번만 센다), 다 채우면 비운다
	refill  bool          // 채우는 중 다시 연결됨 — 끝나면 한 번 더
}

func newTUI(ctx context.Context, a *app, s *session) *tuiModel {
	vp := viewport.New(0, 0)
	vp.KeyMap = viewport.KeyMap{ // 기본 KeyMap의 j/k/d/u/f/b/h/l은 목록 키와 겹친다
		PageDown: key.NewBinding(key.WithKeys("pgdown", " ")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
	}
	m := &tuiModel{ctx: ctx, a: a, s: s, vp: vp, ti: textinput.New()}
	m.dialer, m.liveErr = liveDialer(s)
	return m
}

func (m *tuiModel) Init() tea.Cmd {
	cmd := m.fill(true)
	if m.liveErr != nil { // 이유는 처음 한 번, 그 뒤엔 상태줄 앞 "실시간 꺼짐"
		m.status = m.liveErr.Error()
	}
	return cmd
}

// fill: DM·그룹 DM을 채우고, channels면 이어서 채널(fillChannels). 첫 채우기는 세션 캐시를 채우고(newWatcher)
// 뮤트 목록을 받고 실시간 소스를 띄운 뒤 훑는다 — 채우는 동안 온 것도 잡게.
// 잠금 없이 되는 이유: 캐시 쓰기는 소스를 띄우기 전에 끝나고, 훑기(unreadRows)·소스는 캐시를 읽기만 한다.
// 그래서 Update는 첫 liveMsg부터 세션을 써도 된다. R에서 캐시를 다시 받게 바꾸면 이것이 깨진다.
func (m *tuiModel) fill(channels bool) tea.Cmd {
	m.filling, m.status = true, ""
	ctx, a, s, w, muted, push, dialer, live := m.ctx, m.a, m.s, m.w, m.muted, m.push, m.dialer, m.liveErr == nil
	return func() tea.Msg {
		if w == nil {
			var err error
			if w, err = a.newWatcher(ctx, s); err != nil {
				return filledMsg{err: err}
			}
			w.all = true // 가입 채널의 일반 글도 — 채널 구역
			if s.prof.Kind == "session" {
				if muted, err = s.mutedChannels(ctx); err != nil {
					fmt.Fprintf(a.errw, "뮤트 목록 실패(%v) — 뮤트 채널도 보인다\n", err)
				}
			}
			if live {
				go func() {
					err := a.listen(ctx, s, dialer, func(e *slackevents.MessageEvent) error {
						if show, alert := inbox(w, muted, e); show {
							push(liveMsg{e, alert})
						}
						return nil
					}, func() { push(reconnectedMsg{}) })
					push(liveDownMsg{err})
				}()
			}
		}
		msg := scan(ctx, a, s, w, muted, core.KindIM, core.KindMPIM)
		msg.next, msg.w, msg.muted = channels, w, muted
		return msg
	}
}

// inbox는 실시간 메시지를 받은편지함에 넣을지(show)와 나에게 온 것인지(alert)다. 소스 고루틴에서 부른다 — w·muted는 읽기만.
// 일반 글은 가입 채널의 최상위 글만: 뮤트 채널과 남의 스레드 답글은 뺀다(Slack 사이드바도 그것으로 채널을 굵게 하지 않는다).
func inbox(w *watcher, muted map[string]bool, e *slackevents.MessageEvent) (show, alert bool) {
	show, alert = w.keep(e)
	if !show || alert {
		return show, alert
	}
	reply := e.Message.ThreadTimestamp != "" && e.Message.ThreadTimestamp != e.Message.Timestamp && e.SubType != "thread_broadcast"
	return !muted[e.Channel] && !reply, false
}

// fillChannels는 가입 채널을 채운다(user 프로필은 몇 분). 뮤트 채널은 나에게 온 것이 없으면 뺀다.
func (m *tuiModel) fillChannels() tea.Cmd {
	ctx, a, s, w, muted := m.ctx, m.a, m.s, m.w, m.muted
	return func() tea.Msg { return scan(ctx, a, s, w, muted, core.KindPublic, core.KindPrivate) }
}

func scan(ctx context.Context, a *app, s *session, w *watcher, muted map[string]bool, kinds ...string) filledMsg {
	scope := []core.Channel{}
	for _, c := range s.chans {
		if slices.Contains(kinds, c.Kind) {
			scope = append(scope, c)
		}
	}
	items, err := a.unreadRows(ctx, s, scope, w)
	items = slices.DeleteFunc(items, func(it *unreadItem) bool { return muted[it.ch.ID] && !it.forMe })
	return filledMsg{scope: scope, items: items, err: err}
}

func (m *tuiModel) preview(r tuiRow) tea.Cmd {
	m.seq++
	seq, ctx, s := m.seq, m.ctx, m.s
	head := r.name
	if r.thread() {
		head += " — 스레드"
	}
	return func() tea.Msg {
		if r.thread() {
			msgs, err := s.replies(ctx, r.ch.ID, r.latest.ThreadTS)
			return previewMsg{seq, head, r.ch.ID, msgs, err, "", ""}
		}
		res, err := s.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: r.ch.ID, Limit: 20})
		if err != nil {
			return previewMsg{seq, head, r.ch.ID, nil, err, "", ""}
		}
		var read string
		if info, err := s.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: r.ch.ID}); err == nil {
			read = info.LastRead // 실패면 맨 아래 — 미리보기는 그대로 보인다
		}
		var latest string
		if len(res.Messages) > 0 {
			latest = res.Messages[0].Timestamp
		}
		slices.Reverse(res.Messages) // 최신순 → 아래가 최신
		return previewMsg{seq, head, r.ch.ID, res.Messages, nil, latest, read}
	}
}

// send는 답장을 보내고 읽음 처리한다. DM 본문이면 보낸 메시지까지(응답의 channel·ts — 부모가 지워져 최상위로 갈 수 있다).
// 스레드 답글 줄은 mark하지 않고(스레드 읽음은 비공개 API), 채널 줄은 그 메시지까지 — 답글 ts로 mark하면 사이의 안 본 메시지까지 읽음이 된다.
func (m *tuiModel) send(text string) tea.Cmd {
	m.sending, m.status = true, ""
	ctx, s, r := m.ctx, m.s, *m.reply
	return func() tea.Msg {
		opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
		if th := replyThread(r); th != "" {
			opts = append(opts, slack.MsgOptionTS(th))
		}
		ch, ts, _, err := s.api.PostMessageWithResponseContext(ctx, r.ch.ID, opts...)
		if err != nil {
			return sentMsg{row: r, err: err}
		}
		switch {
		case r.thread():
			return sentMsg{r, r.latest.TS, nil, nil}
		case replyThread(r) == "":
			return sentMsg{r, ts, nil, s.markRead(ctx, ch, ts)}
		}
		return sentMsg{r, r.latest.TS, nil, s.markRead(ctx, r.ch.ID, r.latest.TS)}
	}
}

// mark는 줄의 최신 메시지까지, 그 대화를 미리보기로 봤으면 거기 최신까지 치운다. 스레드 답글 줄은 TUI 안에서만.
func (m *tuiModel) mark(r tuiRow) tea.Cmd {
	m.status = ""
	if r.thread() {
		return func() tea.Msg { return markedMsg{r.ch.ID, r.latest.TS, nil} }
	}
	ts := r.latest.TS
	if m.pv.ch == r.ch.ID {
		ts = max(ts, m.pv.ts) // 고정폭 ts라 문자열 비교
	}
	ctx, s := m.ctx, m.s
	return func() tea.Msg { return markedMsg{r.ch.ID, ts, s.markRead(ctx, r.ch.ID, ts)} }
}

// markRead는 markTo의 "이미 더 뒤까지 읽음" 거절을 성공으로 본다 — 치우려던 것은 이미 치워져 있다.
func (s *session) markRead(ctx context.Context, ch, ts string) error {
	err := s.markTo(ctx, ch, ts)
	if u := (usageError{}); errors.As(err, &u) { // markTo의 usageError는 그 거절뿐이다
		return nil
	}
	return err
}

// rejected는 Slack이 요청을 거절한 확실한 실패다. 그 밖(타임아웃·네트워크·5xx)은 보내졌을 수도 있다.
func rejected(err error) bool {
	var se slack.SlackErrorResponse
	var rl *slack.RateLimitedError
	var sc slack.StatusCodeError
	return errors.As(err, &se) || errors.As(err, &rl) || (errors.As(err, &sc) && sc.Code < 500)
}

// openURL은 변수다 — 테스트가 바꾼다.
var openURL = func(u string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("macOS만")
	}
	return exec.Command("open", u).Run()
}

// setDone은 ts까지 읽은 대화의 줄을 치운다. 그 사이 R로 더 새 메시지가 든 줄이면 둔다.
func (m *tuiModel) setDone(ch, ts string) {
	if i := slices.IndexFunc(m.rows, func(r tuiRow) bool { return r.ch.ID == ch }); i >= 0 && m.rows[i].latest.TS <= ts {
		m.rows[i].done = true
	}
}

// row는 줄 하나를 만든다 — 이름·본문은 여기서 해석해 둔다(View는 세션을 건드리지 않는다).
func (m *tuiModel) row(ch core.Channel, count int, more, forMe bool, raw slack.Message) tuiRow {
	r := tuiRow{ch: ch, count: count, more: more, forMe: forMe, latest: m.s.message(m.ctx, raw, ch.ID)}
	r.name = "#" + ch.Name
	if ch.Kind == core.KindIM {
		r.name = "@" + r.latest.UserName // 실행 중 새로 열린 봇 DM은 ch.User가 없다
		if ch.User != "" {
			r.name = "@" + m.s.userName(m.ctx, ch.User)
		}
	}
	r.text = m.s.render(m.ctx, r.latest.Text)
	return r
}

func (m *tuiModel) rowIndex(ch string) int {
	return slices.IndexFunc(m.rows, func(r tuiRow) bool { return r.ch.ID == ch })
}

// setRows는 "나에게" 먼저, 그 안에서 최신순으로 줄을 바꾸고 커서는 id 대화를 따라간다.
func (m *tuiModel) setRows(id string, rows []tuiRow) {
	slices.SortStableFunc(rows, func(x, y tuiRow) int {
		if x.forMe != y.forMe {
			if x.forMe {
				return -1
			}
			return 1
		}
		return strings.Compare(y.latest.TS, x.latest.TS)
	})
	m.rows = rows
	m.cur = max(m.rowIndex(id), 0)
}

func (m *tuiModel) curID() string {
	if len(m.rows) == 0 {
		return ""
	}
	return m.rows[m.cur].ch.ID
}

// apply는 실시간 메시지를 받은편지함에 합친다. 줄에 이미 같은/더 새 ts가 있으면 무시 — 채우기와 겹친 것·재전송을 두 번 세지 않는다.
// 안 치운 "나에게" 줄에 일반 글은 무시한다 — latest를 나에게 온 것으로 둔다(그래서 ts 가드도 그것끼리 비교된다).
// ponytail: 그 사이 일반 글은 count에 안 든다 — 다음 채우기가 바로잡는다. 정확히 세려면 본 ts 필드 하나.
func (m *tuiModel) apply(e *slackevents.MessageEvent, alert, notify bool) tea.Cmd {
	raw := slack.Message{Msg: *e.Message}
	i := m.rowIndex(e.Channel)
	if i >= 0 && (raw.Timestamp <= m.rows[i].latest.TS || m.rows[i].forMe && !m.rows[i].done && !alert) {
		return nil
	}
	id, rows := m.curID(), m.rows
	var r tuiRow
	if i < 0 {
		ch := core.Channel{ID: e.Channel, Name: e.Channel, Kind: e.ChannelType}
		if j := slices.IndexFunc(m.s.chans, func(c core.Channel) bool { return c.ID == e.Channel }); j >= 0 {
			ch = m.s.chans[j]
		} else if ch.Kind == core.KindIM {
			ch.User = e.User
		}
		r = m.row(ch, 1, false, alert, raw)
	} else {
		old := m.rows[i]
		if old.done { // 치운 뒤 온 것만 센다
			old.count, old.more, old.forMe = 0, false, false
		}
		r = m.row(old.ch, old.count+1, old.more, alert || old.forMe, raw)
		rows = slices.Delete(slices.Clone(rows), i, i+1)
	}
	m.setRows(id, append(rows, r))
	if !notify || !alert || !m.notify {
		return nil
	}
	title, body := r.name, preview(r.text, 120)
	return func() tea.Msg {
		if err := notifyFn(title, body); err != nil {
			return statusMsg("알림 실패: " + err.Error())
		}
		return nil
	}
}

// merge는 채우기 결과로 줄을 바꾼다. 채우기가 못 보는 줄(history에 없는 스레드 답글, scope 밖 대화 — 다른 단계·실행 중 새 DM)은
// 안 치웠으면 남기고, 같은 대화면 더 새 쪽을 — "나에게" 줄은 "채널" 채우기 줄에 지지 않는다. 치운 줄보다 새 것이 없는 채우기 결과(늦게 반영된 mark)는 치운 채로.
// next(뒤에 채널 단계가 있음)면 scope 밖 치운 줄도 남긴다 — 그 단계가 늦게 반영된 mark를 가려야 한다.
func (m *tuiModel) merge(items []*unreadItem, scope []core.Channel, next bool) {
	rows := make([]tuiRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, m.row(it.ch, it.count, it.more, it.forMe, it.latest))
	}
	for _, old := range m.rows {
		i := slices.IndexFunc(rows, func(r tuiRow) bool { return r.ch.ID == old.ch.ID })
		inScope := slices.ContainsFunc(scope, func(c core.Channel) bool { return c.ID == old.ch.ID })
		if old.done {
			switch {
			case i >= 0 && rows[i].latest.TS <= old.latest.TS:
				rows[i].done = true
			case i < 0 && !inScope && next:
				rows = append(rows, old)
			}
			continue
		}
		hidden := old.thread() && old.latest.Subtype != "thread_broadcast"
		if !hidden && inScope {
			continue
		}
		switch {
		case i < 0:
			rows = append(rows, old)
		case old.latest.TS > rows[i].latest.TS || old.forMe && !rows[i].forMe:
			rows[i] = old
		}
	}
	m.setRows(m.curID(), rows)
}

func (m *tuiModel) closeReply() {
	m.reply = nil
	m.ti.Reset()
	m.ti.Blur()
}

// replyKey: 답장 중엔 목록 키보다 먼저 받는다 — 아니면 q·j·space가 목록·viewport로 간다.
func (m *tuiModel) replyKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case k.String() == "ctrl+c":
		return m, tea.Quit
	case m.sending: // 결과를 기다린다 — 보내는 동안 본문이 바뀌지 않게
		return m, nil
	case k.Type == tea.KeyEnter:
		text := strings.TrimSpace(m.ti.Value())
		if text == "" {
			m.status = "본문이 비었다"
			return m, nil
		}
		return m, m.send(text)
	case k.Type == tea.KeyEsc:
		m.closeReply()
		return m, nil
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(k)
	return m, cmd
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.listH = msg.Height * 2 / 5
		m.vp.Width, m.vp.Height = msg.Width, max(msg.Height-m.listH-3, 1) // 구분선 둘 + 상태줄
		m.ti.Width = max(msg.Width-lipgloss.Width(m.ti.Prompt)-1, 1)      // 0이면 긴 본문이 두 줄이 된다
	case statusMsg:
		if strings.HasPrefix(string(msg), "\r") { // scanUnread 진행
			m.progress = strings.TrimSpace(string(msg))
		} else {
			m.status = strings.TrimSpace(string(msg))
		}
	case filledMsg:
		if m.w == nil {
			m.w, m.muted = msg.w, msg.muted
		}
		if msg.err != nil {
			m.status = "채우기 실패: " + msg.err.Error()
		} else {
			m.merge(msg.items, msg.scope, msg.next)
		}
		for _, l := range m.pending { // 처음 적용 때 이미 알렸다
			m.apply(l.e, l.alert, false)
		}
		if msg.next && m.w != nil { // DM 실패여도 채널은 — ctx 취소면 어차피 곧 실패한다
			return m, m.fillChannels()
		}
		m.filling, m.pending = false, nil
		if m.refill {
			m.refill = false
			return m, m.fill(false)
		}
	case liveMsg:
		if m.filling {
			m.pending = append(m.pending, msg)
		}
		return m, m.apply(msg.e, msg.alert, true)
	case reconnectedMsg: // 끊긴 사이 온 DM은 채우기가 되찾는다. 채널까지 매번 훑지는 않는다(user는 몇 분) — 채널은 R로
		if m.filling {
			m.refill = true
			return m, nil
		}
		return m, m.fill(false)
	case liveDownMsg:
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			m.liveErr, m.status = msg.err, msg.err.Error()
		}
	case previewMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		if msg.err != nil { // 이전 대화의 미리보기를 남기지 않는다
			m.vp.SetContent(msg.head + "\n미리보기 실패: " + msg.err.Error())
			return m, nil
		}
		out := make([]core.Message, len(msg.msgs))
		for i, sm := range msg.msgs {
			out[i] = m.s.message(m.ctx, sm, msg.ch)
		}
		var b bytes.Buffer
		fmt.Fprintln(&b, msg.head)
		// 안 본 첫 메시지를 맨 위에(위로 스크롤하면 앞 맥락). 안 본 부분을 따로 찍어 날짜 줄이 경계 표시가 된다.
		i := slices.IndexFunc(out, func(cm core.Message) bool { return msg.read != "" && cm.TS > msg.read })
		if i < 0 {
			i = len(out)
		}
		m.s.printMessages(m.ctx, &b, out[:i])
		top := strings.Count(b.String(), "\n")
		m.s.printMessages(m.ctx, &b, out[i:])
		m.vp.SetContent(b.String())
		if m.vp.GotoBottom(); i < len(out) {
			m.vp.SetYOffset(top) // 안 본 것이 화면보다 짧으면 viewport가 맨 아래로 맞춘다
		}
		if msg.latest != "" {
			m.pv.ch, m.pv.ts = msg.ch, msg.latest
		}
	case sentMsg:
		m.sending = false
		r := msg.row
		switch {
		case msg.err != nil && rejected(msg.err): // 입력은 남긴다
			m.status = "보내기 실패: " + msg.err.Error()
			return m, nil
		case msg.err != nil:
			m.status = "전송 여부 불확실 — 미리보기 확인(그대로 Enter면 두 번 갈 수 있다): " + msg.err.Error()
			return m, m.preview(r)
		case msg.markErr != nil:
			m.status = "보냈다 — 읽음 처리 실패(m으로 다시): " + msg.markErr.Error()
		default:
			m.status = "보냈다"
			m.setDone(r.ch.ID, msg.ts)
		}
		m.closeReply()
		return m, m.preview(r)
	case markedMsg:
		if msg.err != nil {
			m.status = "치우기 실패: " + msg.err.Error()
			return m, nil
		}
		m.setDone(msg.ch, msg.ts)
	case tea.KeyMsg:
		if m.reply != nil {
			return m.replyKey(msg)
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.cur = min(m.cur+1, max(len(m.rows)-1, 0))
		case "k", "up":
			m.cur = max(m.cur-1, 0)
		case "R":
			if m.filling {
				m.status = "채우는 중 — 끝나면 R"
				return m, nil
			}
			return m, m.fill(true)
		case "enter", "r", "m", "o":
			if len(m.rows) == 0 {
				break
			}
			r := m.rows[m.cur]
			switch msg.String() {
			case "enter":
				return m, m.preview(r)
			case "r":
				m.reply, m.status = &r, ""
				m.ti.Prompt = " 답장 " + r.name + " > "
				if replyThread(r) != "" {
					m.ti.Prompt = " 답장 " + r.name + " ↳ > "
				}
				m.ti.Width = max(m.vp.Width-lipgloss.Width(m.ti.Prompt)-1, 1)
				return m, m.ti.Focus()
			case "m":
				return m, m.mark(r)
			case "o":
				u := r.latest.Permalink
				return m, func() tea.Msg {
					if openURL(u) != nil {
						return statusMsg("열 수 없다 — " + u)
					}
					return nil
				}
			}
		default:
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
	default: // 커서 깜박임 등
		if m.reply != nil {
			var cmd tea.Cmd
			m.ti, cmd = m.ti.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

var (
	nameStyle = lipgloss.NewStyle().Width(24).MaxWidth(24).Inline(true) // Inline 없으면 자르지 않고 줄바꿈한다
	curStyle  = lipgloss.NewStyle().Reverse(true)
	doneStyle = lipgloss.NewStyle().Faint(true)
)

func (m *tuiModel) View() string {
	var b strings.Builder
	line := lipgloss.NewStyle().MaxWidth(m.vp.Width).Inline(true) // 여러 줄 오류가 와도 한 줄
	var lines []string                                            // 머리줄 + 줄
	cur, forMe, chans := 0, 0, 0
	for _, r := range m.rows {
		if !r.done && r.forMe {
			forMe++
		} else if !r.done {
			chans++
		}
	}
	for i, r := range m.rows {
		if i == 0 {
			lines = append(lines, fmt.Sprintf(" 나에게 (%d)", forMe))
		}
		if !r.forMe && (i == 0 || m.rows[i-1].forMe) {
			lines = append(lines, fmt.Sprintf(" 채널 (%d)", chans))
		}
		name := r.name
		if r.thread() {
			name += " ↳"
		}
		n := fmt.Sprint(r.count)
		if r.more {
			n += "+"
		}
		dot := "●"
		switch {
		case r.done:
			dot = " "
		case !r.forMe:
			dot = "○"
		}
		l := line.Render(fmt.Sprintf("%s %s %4s  %s  @%s: %s", dot, nameStyle.Render(name), n,
			core.Time(r.latest.TS).Local().Format("01/02 15:04"), r.latest.UserName, preview(r.text, 200)))
		if r.done || !r.forMe {
			l = doneStyle.Render(l)
		}
		if i == m.cur {
			l, cur = curStyle.Render(l), len(lines)
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 && !m.filling && m.w != nil {
		lines = append(lines, " 나에게 온 것이 없다")
	}
	top := max(cur-m.listH+1, 0) // 커서가 보이게
	for i := top; i < top+m.listH; i++ {
		if i < len(lines) {
			b.WriteString(lines[i])
		}
		b.WriteString("\n")
	}
	sep := strings.Repeat("─", m.vp.Width)
	b.WriteString(sep + "\n" + m.vp.View() + "\n")
	keys := "j/k 이동  Enter 열기  r 답장  m 치우기  o 브라우저  R 새로고침  q 끝"
	if m.reply != nil { // 아래 구분선 자리에 입력줄 — 높이는 그대로
		sep, keys = m.ti.View(), "Enter 보내기  Esc 취소"
	}
	b.WriteString(line.Render(sep) + "\n")
	status := m.status
	switch {
	case m.sending:
		status = "보내는 중…"
	case m.filling:
		status = cmp.Or(status, m.progress, "채우는 중…")
	}
	if m.liveErr != nil {
		status = strings.TrimSpace("실시간 꺼짐   " + status)
	}
	if status != "" { // 상태가 먼저 — 키 힌트는 잘려도 된다
		status += "   "
	}
	b.WriteString(line.Render(" " + status + keys))
	return b.String()
}
