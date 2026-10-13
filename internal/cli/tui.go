package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
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
		Short: "받은편지함 화면 — 밀린 DM·그룹 DM, Enter로 미리보기",
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
		seq  int
		head string
		ch   string
		msgs []slack.Message
		err  error
	}
)

type tuiRow struct {
	ch         core.Channel
	name, text string // Update에서 해석해 둔다 — View는 세션을 건드리지 않는다
	count      int
	more       bool
	latest     core.Message
}

// 스레드 답글이면 미리보기는 그 스레드다(history에는 답글이 없어 T1에선 thread_broadcast만).
func (r tuiRow) thread() bool { return r.latest.ThreadTS != "" && r.latest.ThreadTS != r.latest.TS }

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
	seq     int // Enter마다 늘린다 — 늦게 온 미리보기를 버린다
	listH   int
	vp      viewport.Model
	status  string
}

func newTUI(ctx context.Context, a *app, s *session) *tuiModel {
	vp := viewport.New(0, 0)
	vp.KeyMap = viewport.KeyMap{ // 기본 KeyMap의 j/k/d/u/f/b/h/l은 목록 키와 겹친다
		PageDown: key.NewBinding(key.WithKeys("pgdown", " ")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
	}
	return &tuiModel{ctx: ctx, a: a, s: s, vp: vp}
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
			return previewMsg{seq, head, r.ch.ID, msgs, err}
		}
		res, err := s.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: r.ch.ID, Limit: 20})
		if err != nil {
			return previewMsg{seq, head, r.ch.ID, nil, err}
		}
		slices.Reverse(res.Messages) // 최신순 → 아래가 최신
		return previewMsg{seq, head, r.ch.ID, res.Messages, nil}
	}
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.listH = msg.Height * 2 / 5
		m.vp.Width, m.vp.Height = msg.Width, max(msg.Height-m.listH-3, 1) // 구분선 둘 + 상태줄
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
	case tea.KeyMsg:
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
		case "enter":
			if len(m.rows) > 0 {
				return m, m.preview(m.rows[m.cur])
			}
		default:
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

var (
	nameStyle = lipgloss.NewStyle().Width(24).MaxWidth(24).Inline(true) // Inline 없으면 자르지 않고 줄바꿈한다
	curStyle  = lipgloss.NewStyle().Reverse(true)
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
			l := line.Render(fmt.Sprintf("● %s %4s  %s  @%s: %s", nameStyle.Render(name), n,
				core.Time(r.latest.TS).Local().Format("01/02 15:04"), r.latest.UserName, preview(r.text, 200)))
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
	b.WriteString(sep + "\n" + m.vp.View() + "\n" + sep + "\n")
	status := m.status
	if m.filling {
		status = "채우는 중…"
	}
	b.WriteString(line.Render(" j/k 이동  Enter 열기  R 새로고침  q 끝   " + status))
	return b.String()
}
