package cli

import (
	"bytes"
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
	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
	"github.com/spf13/cobra"
)

func (a *app) tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "받은편지함 화면 — 밀린 DM·그룹 DM을 보고, 답하고(r), 치운다(m)",
		Args:  cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, _ []string) error {
			if a.asJSON {
				return usagef("tui는 --json을 쓰지 않는다")
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
			p := tea.NewProgram(newTUI(ctx, &ta, s), tea.WithContext(ctx), tea.WithAltScreen(), tea.WithOutput(a.out))
			ta.errw = statusWriter(p.Send)
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
		chans []core.Channel
		items []*unreadItem
		err   error
	}
	previewMsg struct {
		seq    int
		head   string
		ch     string
		msgs   []slack.Message
		err    error
		latest string // history 미리보기의 최신 ts — 스레드 미리보기는 ""(답글 ts로 대화를 mark하면 사이의 안 본 메시지까지 읽음이 된다)
	}
	sentMsg struct {
		row          tuiRow
		ts           string // 보낸 메시지
		err, markErr error
	}
	markedMsg struct {
		ch, ts string
		err    error
	}
)

type tuiRow struct {
	ch         core.Channel
	name, text string // Update에서 해석해 둔다 — View는 세션을 건드리지 않는다
	count      int
	more, done bool // done: 치움(m 또는 답장 뒤 읽음)
	latest     core.Message
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

type tuiReply struct {
	row    tuiRow // 값 복사 — 입력 중 filledMsg가 줄을 바꿔도 대상은 그대로
	thread string
}

// tuiModel: Cmd(고루틴)는 Slack API만 부르고, 이름 해석·렌더링은 Update(한 고루틴)에서 한다.
// 세션 캐시는 첫 채우기 Cmd만 채우고, Update는 첫 filledMsg 전엔 세션을 건드리지 않는다.
type tuiModel struct {
	ctx     context.Context
	a       *app
	s       *session
	chans   []core.Channel // 첫 채우기 전엔 nil
	rows    []tuiRow
	cur     int
	filling bool
	seq     int                     // Enter마다 늘린다 — 늦게 온 미리보기를 버린다
	pv      struct{ ch, ts string } // 마지막 history 미리보기의 최신 ts — m이 본 데까지 치운다
	listH   int
	vp      viewport.Model
	ti      textinput.Model
	reply   *tuiReply // 답장 입력 중
	sending bool
	status  string
}

func newTUI(ctx context.Context, a *app, s *session) *tuiModel {
	vp := viewport.New(0, 0)
	vp.KeyMap = viewport.KeyMap{ // 기본 KeyMap의 j/k/d/u/f/b/h/l은 목록 키와 겹친다
		PageDown: key.NewBinding(key.WithKeys("pgdown", " ")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
	}
	return &tuiModel{ctx: ctx, a: a, s: s, vp: vp, ti: textinput.New()}
}

func (m *tuiModel) Init() tea.Cmd { return m.fill() }

func (m *tuiModel) fill() tea.Cmd {
	m.filling, m.status = true, ""
	ctx, a, s, chans := m.ctx, m.a, m.s, m.chans
	return func() tea.Msg {
		if chans == nil {
			if err := s.loadChannels(ctx, false); err != nil {
				return filledMsg{err: err}
			}
			if err := s.loadUsers(ctx, false); err != nil { // 이름 프리페치일 뿐 — 실패해도 이름은 ID로 나온다(unread와 같게)
				fmt.Fprintf(a.errw, "사용자 목록 실패(%v)\n", err)
			}
			chans = []core.Channel{}
			for _, c := range s.chans {
				if c.Kind == core.KindIM || c.Kind == core.KindMPIM {
					chans = append(chans, c)
				}
			}
		}
		items, err := a.unreadRows(ctx, s, chans)
		return filledMsg{chans, items, err}
	}
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
			return previewMsg{seq, head, r.ch.ID, msgs, err, ""}
		}
		res, err := s.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: r.ch.ID, Limit: 20})
		if err != nil {
			return previewMsg{seq, head, r.ch.ID, nil, err, ""}
		}
		var latest string
		if len(res.Messages) > 0 {
			latest = res.Messages[0].Timestamp
		}
		slices.Reverse(res.Messages) // 최신순 → 아래가 최신
		return previewMsg{seq, head, r.ch.ID, res.Messages, nil, latest}
	}
}

// send는 답장을 보내고 보낸 메시지까지 읽음 처리한다. mark는 응답의 channel·ts로(부모가 지워져 최상위로 갈 수 있다).
func (m *tuiModel) send(text string) tea.Cmd {
	m.sending, m.status = true, ""
	ctx, s, r := m.ctx, m.s, *m.reply
	return func() tea.Msg {
		opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
		if r.thread != "" {
			opts = append(opts, slack.MsgOptionTS(r.thread))
		}
		ch, ts, _, err := s.api.PostMessageWithResponseContext(ctx, r.row.ch.ID, opts...)
		if err != nil {
			return sentMsg{row: r.row, err: err}
		}
		return sentMsg{r.row, ts, nil, s.markRead(ctx, ch, ts)}
	}
}

// mark는 줄의 최신 메시지까지, 그 대화를 미리보기로 봤으면 거기 최신까지 치운다.
func (m *tuiModel) mark(r tuiRow) tea.Cmd {
	m.status = ""
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
		m.status = strings.TrimSpace(string(msg))
	case filledMsg:
		m.filling = false
		if msg.err != nil {
			m.status = "채우기 실패: " + msg.err.Error()
			return m, nil
		}
		m.chans = msg.chans
		var id string
		if len(m.rows) > 0 {
			id = m.rows[m.cur].ch.ID
		}
		m.rows = m.rows[:0]
		for _, it := range msg.items {
			r := tuiRow{ch: it.ch, count: it.count, more: it.more, latest: m.s.message(m.ctx, it.latest, it.ch.ID)}
			r.name = "#" + it.ch.Name
			if it.ch.Kind == core.KindIM {
				r.name = "@" + m.s.userName(m.ctx, it.ch.User)
			}
			r.text = m.s.render(m.ctx, r.latest.Text)
			m.rows = append(m.rows, r)
		}
		m.cur = max(slices.IndexFunc(m.rows, func(r tuiRow) bool { return r.ch.ID == id }), 0)
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
		m.s.printMessages(m.ctx, &b, out)
		m.vp.SetContent(b.String())
		m.vp.GotoBottom()
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
			if !m.filling {
				return m, m.fill()
			}
		case "enter", "r", "m", "o":
			if len(m.rows) == 0 {
				break
			}
			r := m.rows[m.cur]
			switch msg.String() {
			case "enter":
				return m, m.preview(r)
			case "r":
				m.reply, m.status = &tuiReply{r, replyThread(r)}, ""
				m.ti.Prompt = " 답장 " + r.name + " > "
				if m.reply.thread != "" {
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
	top := max(m.cur-m.listH+1, 0)                                // 커서가 보이게
	for i := top; i < top+m.listH; i++ {
		switch {
		case i < len(m.rows):
			r := m.rows[i]
			name := r.name
			if r.thread() {
				name += " ↳"
			}
			n := fmt.Sprint(r.count)
			if r.more {
				n += "+"
			}
			dot := "●"
			if r.done {
				dot = " "
			}
			l := line.Render(fmt.Sprintf("%s %s %4s  %s  @%s: %s", dot, nameStyle.Render(name), n,
				core.Time(r.latest.TS).Local().Format("01/02 15:04"), r.latest.UserName, preview(r.text, 200)))
			if r.done {
				l = doneStyle.Render(l)
			}
			if i == m.cur {
				l = curStyle.Render(l)
			}
			b.WriteString(l)
		case i == 0 && !m.filling && m.chans != nil:
			b.WriteString(" 밀린 DM·그룹 DM이 없다")
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
		status = "채우는 중…"
	}
	if status != "" { // 상태가 먼저 — 키 힌트는 잘려도 된다
		status += "   "
	}
	b.WriteString(line.Render(" " + status + keys))
	return b.String()
}
