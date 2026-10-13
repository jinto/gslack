package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
)

func press(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestTUI(t *testing.T) {
	setup(t)
	ctx := context.Background()
	a := &app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}
	s, err := a.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(ctx, a, s)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// 채우기 전: 빈 목록에서 Enter·이동은 아무것도 안 한다.
	if _, cmd := m.Update(press("enter")); cmd != nil {
		t.Error("빈 목록 Enter가 Cmd를 냈다")
	}
	m.Update(press("j"))

	fill := m.Init()
	if _, cmd := m.Update(press("R")); cmd != nil {
		t.Error("채우는 중 R이 무시되지 않았다")
	}
	m.Update(fill())
	if len(m.rows) != 1 || m.rows[0].ch.ID != "D3UNREADDM" || !strings.Contains(m.View(), "@지수") {
		t.Fatalf("rows %v\n%s", m.rows, m.View())
	}

	_, cmd := m.Update(press("enter"))
	m.Update(previewMsg{seq: m.seq - 1, head: "낡은"})
	m.Update(cmd())
	if v := m.View(); !strings.Contains(v, "안 읽은 DM") || strings.Contains(v, "낡은") {
		t.Errorf("미리보기:\n%s", v)
	}

	// 채우기가 실패해도 R은 다시 된다.
	_, cmd = m.Update(press("R"))
	m.Update(filledMsg{err: errors.New("boom")})
	if !strings.Contains(m.View(), "boom") {
		t.Errorf("실패가 상태줄에 없다:\n%s", m.View())
	}
	if _, cmd = m.Update(press("R")); cmd == nil {
		t.Error("실패 뒤 R이 무시됐다")
	}
	m.Update(cmd())
	if len(m.rows) != 1 {
		t.Errorf("다시 채우기: %v", m.rows)
	}

	if _, cmd := m.Update(press("q")); cmd == nil || cmd() != tea.Quit() {
		t.Error("q가 끝내지 않는다")
	}
}

func TestTUIThreadPreview(t *testing.T) {
	setup(t)
	ctx := context.Background()
	a := &app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}
	s, err := a.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(ctx, a, s)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(filledMsg{chans: s.chans, items: []*unreadItem{{ch: core.Channel{ID: "C1GENERAL0", Name: "general"}, count: 1,
		latest: slack.Message{Msg: slack.Msg{Timestamp: "1700000002.500000", ThreadTimestamp: "1700000002.000000", User: "U1AAAAAAAA"}}},
		{ch: core.Channel{ID: "G2MPDMMPDM", Name: "mpdm-alice--bob--carol--dave-1", Kind: core.KindMPIM}, count: 1,
			latest: slack.Message{Msg: slack.Msg{Timestamp: "1700000002.500000", ThreadTimestamp: "1700000002.000000", User: "U1AAAAAAAA"}}}}})
	_, cmd := m.Update(press("enter"))
	m.Update(cmd())
	m.Update(statusMsg("여러\n줄 오류"))
	if v := m.View(); strings.Count(v, "\n") != 23 || strings.Contains(v, "dave") { // 24줄 화면: 긴 이름은 잘리고, 여러 줄 상태는 한 줄로
		t.Errorf("레이아웃:\n%s", v)
	}
	if v := m.View(); !strings.Contains(v, "#general ↳") || !strings.Contains(v, "#general — 스레드") || !strings.Contains(v, "parent") {
		t.Errorf("스레드 미리보기:\n%s", v)
	}
}

func TestTUIUsage(t *testing.T) {
	setup(t)
	for _, args := range [][]string{{"tui", "--json"}, {"tui"}} { // 테스트 stdout은 터미널이 아니다
		if code, _, _ := gslack(args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func typeText(m *tuiModel, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// filledTUI는 D3UNREADDM(지수, latest …03) 한 줄로 채운 모델이다.
func filledTUI(t *testing.T) (*fakeSlack, *tuiModel) {
	f := setup(t)
	ctx := context.Background()
	a := &app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}
	s, err := a.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(ctx, a, s)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(m.Init()())
	if len(m.rows) != 1 {
		t.Fatalf("rows %v", m.rows)
	}
	return f, m
}

func TestTUIReply(t *testing.T) {
	f, m := filledTUI(t)
	m.Update(press("r"))
	typeText(m, "qj ok") // 목록 키도 글자로 들어간다
	if m.cur != 0 || m.ti.Value() != "qj ok" || !strings.Contains(m.View(), "답장 @지수") {
		t.Fatalf("입력 %q cur %d\n%s", m.ti.Value(), m.cur, m.View())
	}
	_, cmd := m.Update(press("enter"))
	if _, c := m.Update(press("enter")); c != nil || !m.sending { // 보내는 중 두 번째 Enter는 무시
		t.Error("보내는 중 Enter가 무시되지 않았다")
	}
	_, cmd = m.Update(cmd())
	p := f.reqs["chat.postMessage"]
	if len(p) != 1 || p[0].Get("text") != "qj ok" || p[0].Get("thread_ts") != "" || p[0].Get("channel") != "D3UNREADDM" {
		t.Errorf("보낸 것: %v", p)
	}
	if mk := f.reqs["conversations.mark"]; len(mk) != 1 || mk[0].Get("ts") != "1700000099.000000" {
		t.Errorf("자동 읽음: %v", mk)
	}
	if m.reply != nil || !m.rows[0].done || cmd == nil {
		t.Errorf("성공 뒤: reply %v done %v 미리보기 %v", m.reply, m.rows[0].done, cmd != nil)
	}

	// 빈 본문은 막고, Esc는 아무것도 보내지 않는다.
	m.Update(press("r"))
	typeText(m, "  ")
	if _, cmd := m.Update(press("enter")); cmd != nil || !strings.Contains(m.status, "비었다") {
		t.Errorf("빈 본문: %q", m.status)
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); cmd != nil || m.reply != nil || len(f.reqs["chat.postMessage"]) != 1 {
		t.Error("Esc")
	}
}

func TestTUIReplyFailure(t *testing.T) {
	_, m := filledTUI(t)
	for _, c := range []struct {
		err     error
		sure    bool
		in, msg string
	}{
		{slack.SlackErrorResponse{Err: "not_in_channel"}, true, "not_in_channel", "보내기 실패"},
		{&slack.RateLimitedError{}, true, "rate", "보내기 실패"},
		{slack.StatusCodeError{Code: 403}, true, "403", "보내기 실패"},
		{slack.StatusCodeError{Code: 502}, false, "502", "불확실"},
		{context.DeadlineExceeded, false, "deadline", "불확실"},
	} {
		m.Update(press("r"))
		typeText(m, "hi")
		m.Update(press("enter"))
		_, cmd := m.Update(sentMsg{err: c.err})
		if m.sending || m.reply == nil || m.ti.Value() != "hi" || !strings.Contains(m.status, c.msg) || (cmd == nil) != c.sure {
			t.Errorf("%v: reply %v 입력 %q 상태 %q 미리보기 %v", c.err, m.reply != nil, m.ti.Value(), m.status, cmd != nil)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	}
	// 보냈지만 읽음 실패: 입력은 닫고 ●는 남긴다.
	m.Update(press("r"))
	typeText(m, "hi")
	m.Update(press("enter"))
	m.Update(sentMsg{markErr: errors.New("boom")})
	if m.reply != nil || m.rows[0].done || !strings.Contains(m.status, "읽음 처리 실패") {
		t.Errorf("읽음 실패: %v %v %q", m.reply != nil, m.rows[0].done, m.status)
	}
}

func TestReplyThread(t *testing.T) {
	im := core.Channel{Kind: core.KindIM}
	for _, c := range []struct {
		ch           core.Channel
		ts, threadTS string
		want         string
	}{
		{im, "1.0", "", ""},
		{core.Channel{Kind: core.KindMPIM}, "1.0", "", ""},
		{im, "2.0", "1.0", "1.0"}, // 스레드 답글 → 그 스레드
		{im, "1.0", "1.0", ""},    // 답글 달린 부모는 스레드가 아니다(미리보기와 같은 술어)
		{core.Channel{Kind: core.KindPublic}, "1.0", "", "1.0"},
		{core.Channel{Kind: core.KindPrivate}, "1.0", "", "1.0"},
	} {
		if got := replyThread(tuiRow{ch: c.ch, latest: core.Message{TS: c.ts, ThreadTS: c.threadTS}}); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

func TestTUIMark(t *testing.T) {
	f, m := filledTUI(t)
	_, cmd := m.Update(press("enter"))
	m.Update(cmd())
	_, cmd = m.Update(press("m"))
	m.Update(cmd())
	if mk := f.reqs["conversations.mark"]; len(mk) != 1 || mk[0].Get("ts") != "1700000005.000000" || !m.rows[0].done {
		t.Errorf("미리보기 최신까지: %v", mk)
	}
	if strings.Contains(m.View(), "●") {
		t.Errorf("치운 줄에 ●:\n%s", m.View())
	}

	// 이미 더 뒤까지 읽었으면(last_read …02 > …01) mark 없이 치움.
	m.rows[0] = tuiRow{ch: core.Channel{ID: "C1GENERAL0"}, latest: core.Message{TS: "1700000001.000000"}}
	_, cmd = m.Update(press("m"))
	m.Update(cmd())
	if len(f.reqs["conversations.mark"]) != 1 || !m.rows[0].done || m.status != "" {
		t.Errorf("거절=성공: %v %q", f.reqs["conversations.mark"], m.status)
	}
}

func TestTUIOpen(t *testing.T) {
	_, m := filledTUI(t)
	var got string
	orig := openURL
	t.Cleanup(func() { openURL = orig })
	openURL = func(u string) error { got = u; return nil }
	_, cmd := m.Update(press("o"))
	if msg := cmd(); msg != nil || got != m.rows[0].latest.Permalink || got == "" {
		t.Errorf("open %q %v", got, msg)
	}
	openURL = func(string) error { return errors.New("no") }
	_, cmd = m.Update(press("o"))
	m.Update(cmd())
	if !strings.Contains(m.status, got) {
		t.Errorf("실패면 URL: %q", m.status)
	}
}
