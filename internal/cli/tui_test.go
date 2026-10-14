package cli

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
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
		_, cmd := m.Update(sentMsg{row: m.rows[0], err: c.err})
		if m.sending || m.reply == nil || m.ti.Value() != "hi" || !strings.Contains(m.status, c.msg) || (cmd == nil) != c.sure {
			t.Errorf("%v: reply %v 입력 %q 상태 %q 미리보기 %v", c.err, m.reply != nil, m.ti.Value(), m.status, cmd != nil)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	}
	// 보냈지만 읽음 실패: 입력은 닫고 ●는 남긴다.
	m.Update(press("r"))
	typeText(m, "hi")
	m.Update(press("enter"))
	m.Update(sentMsg{row: m.rows[0], ts: "1700000099.000000", markErr: errors.New("boom")})
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

	// 실패는 상태줄에. 치우는 사이 R로 더 새 메시지가 든 줄은 done이 되지 않는다.
	m.rows[0].done = false
	m.Update(markedMsg{"C1GENERAL0", "1700000001.000000", errors.New("boom")})
	m.Update(markedMsg{"C1GENERAL0", "1700000000.000000", nil})
	if m.rows[0].done || !strings.Contains(m.status, "치우기 실패") {
		t.Errorf("실패·낡은 결과: %v %q", m.rows[0].done, m.status)
	}
}

func TestTUIReplyThreadRow(t *testing.T) {
	f, m := filledTUI(t)
	m.rows[0].latest.ThreadTS = "1700000001.000000" // 스레드 답글 줄
	m.Update(press("r"))
	typeText(m, "ok")
	_, cmd := m.Update(press("enter"))
	m.Update(cmd())
	if p := f.reqs["chat.postMessage"]; len(p) != 1 || p[0].Get("thread_ts") != "1700000001.000000" {
		t.Errorf("스레드로: %v", p)
	}
}

func TestTUIEmptyKeys(t *testing.T) {
	setup(t)
	ctx := context.Background()
	a := &app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}
	s, err := a.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(ctx, a, s)
	for _, k := range []string{"r", "m", "o"} {
		if _, cmd := m.Update(press(k)); cmd != nil || m.reply != nil {
			t.Errorf("빈 목록 %s", k)
		}
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

func live(ch, typ, user, ts, text, threadTS string) liveMsg {
	return liveMsg{&slackevents.MessageEvent{Channel: ch, ChannelType: typ, User: user, Text: text,
		Message: &slack.Msg{Channel: ch, User: user, Text: text, Timestamp: ts, ThreadTimestamp: threadTS}}}
}

// TestTUILive: 실시간 메시지 병합 — ts 가드, 새 줄, 치운 줄, 채우는 중 온 것, R 합치기, 알림.
func TestTUILive(t *testing.T) {
	_, m := filledTUI(t) // D3UNREADDM count 1, latest …03
	var notes int
	orig := notifyFn
	t.Cleanup(func() { notifyFn = orig })
	notifyFn = func(string, string) error { notes++; return nil }
	m.notify = true
	note := func(_ tea.Model, cmd tea.Cmd) {
		if cmd != nil {
			cmd()
		}
	}

	note(m.Update(live("D9NEWDMDM0", "im", "U1AAAAAAAA", "1700000010.000000", "새 DM", "")))
	if len(m.rows) != 2 || m.rows[0].ch.ID != "D9NEWDMDM0" || m.rows[0].count != 1 || m.rows[0].name != "@jk" || m.curID() != "D3UNREADDM" {
		t.Fatalf("새 줄: %+v cur %s", m.rows, m.curID())
	}
	note(m.Update(live("D9NEWDMDM0", "im", "U1AAAAAAAA", "1700000010.000000", "재전송", "")))
	note(m.Update(live("D3UNREADDM", "im", "U3HHHHHHHH", "1700000002.000000", "채우기에 든 옛것", "")))
	if m.rows[0].count != 1 || m.rows[1].count != 1 || notes != 1 {
		t.Errorf("ts 가드: %+v 알림 %d", m.rows, notes)
	}
	m.rows[1].done = true // 치운 줄에 새 것 → 1부터
	note(m.Update(live("D3UNREADDM", "im", "U3HHHHHHHH", "1700000011.000000", "또", "")))
	if r := m.rows[0]; r.ch.ID != "D3UNREADDM" || r.count != 1 || r.done || r.text != "또" || m.curID() != "D3UNREADDM" {
		t.Errorf("치운 줄: %+v", r)
	}
	note(m.Update(live("C1GENERAL0", "channel", "U1AAAAAAAA", "1700000012.000000", "<@U0MEMEMEME> 봐 줘", "")))
	if r := m.rows[0]; r.name != "#general" || r.ch.Kind != core.KindPublic || replyThread(r) != "1700000012.000000" {
		t.Errorf("채널 멘션 줄: %+v", r)
	}
	note(m.Update(live("D1DMDMDMDM", "im", "U1AAAAAAAA", "1700000013.000000", "스레드 답글", "1700000001.000000")))
	if notes != 4 {
		t.Errorf("알림 %d회", notes)
	}

	// R: 채우는 중 온 D3 메시지는 바로 보이고, 채우기(D3 latest …03)가 못 봤으니 뒤에도 +1. 알림은 한 번.
	m.rows[slices.IndexFunc(m.rows, func(r tuiRow) bool { return r.ch.ID == "D9NEWDMDM0" })].done = true
	_, fill := m.Update(press("R"))
	note(m.Update(live("D3UNREADDM", "im", "U3HHHHHHHH", "1700000014.000000", "채우는 중", "")))
	if m.rows[0].count != 2 || notes != 5 {
		t.Errorf("채우는 중: %+v 알림 %d", m.rows[0], notes)
	}
	m.Update(fill())
	ids := []string{}
	for _, r := range m.rows {
		ids = append(ids, r.ch.ID)
	}
	// D9(치움) 사라짐, D1 스레드 줄·C1(채우기 대상 밖) 남음. D3는 채우기 1 + 채우는 중 1.
	if strings.Join(ids, ",") != "D3UNREADDM,D1DMDMDMDM,C1GENERAL0" || m.rows[0].count != 2 || m.rows[0].latest.TS != "1700000014.000000" || notes != 5 {
		t.Errorf("R 뒤: %v %+v 알림 %d", ids, m.rows[0], notes)
	}

	// 채우기가 그 메시지를 이미 셌으면 다시 세지 않는다.
	m.Update(press("R"))
	m.Update(live("D3UNREADDM", "im", "U3HHHHHHHH", "1700000015.000000", "x", ""))
	m.Update(filledMsg{chans: m.chans, items: []*unreadItem{{ch: m.rows[0].ch, count: 5,
		latest: slack.Message{Msg: slack.Msg{Timestamp: "1700000015.000000", User: "U3HHHHHHHH"}}}}})
	if r := m.rows[0]; r.count != 5 || len(m.pending) != 0 {
		t.Errorf("채우기가 센 것: %+v", r)
	}
}

func TestTUILiveConn(t *testing.T) {
	f, m := filledTUI(t)
	if _, cmd := m.Update(reconnectedMsg{}); cmd == nil || !m.filling {
		t.Fatal("재연결 → R")
	}
	if _, cmd := m.Update(reconnectedMsg{}); cmd != nil || !m.refill {
		t.Error("채우는 중 재연결은 끝나고 한 번 더")
	}
	if _, cmd := m.Update(filledMsg{chans: m.chans}); cmd == nil || !m.filling || m.refill {
		t.Error("refill")
	}
	m.Update(filledMsg{chans: m.chans})

	// 첫 채우기가 실패해도 chans를 받았으면 R은 newWatcher(users.conversations)를 다시 타지 않는다.
	m2 := newTUI(m.ctx, m.a, m.s)
	m2.Update(filledMsg{chans: []core.Channel{}, err: errors.New("boom")})
	n := len(f.reqs["users.conversations"])
	_, cmd := m2.Update(press("R"))
	m2.Update(cmd())
	if len(f.reqs["users.conversations"]) != n {
		t.Error("R이 첫 채우기를 다시 탔다")
	}

	m.liveErr = nil
	m.Update(liveDownMsg{context.Canceled})
	if m.liveErr != nil {
		t.Error("q로 끝난 건 꺼짐이 아니다")
	}
	m.Update(liveDownMsg{slack.SlackErrorResponse{Err: "invalid_auth"}})
	if v := m.View(); !strings.Contains(v, "실시간 꺼짐") || !strings.Contains(v, "invalid_auth") {
		t.Errorf("꺼짐:\n%s", v)
	}
}

// 스레드 답글 줄은 m·답장 뒤 읽음 모두 API 없이 TUI 안에서만. 채널 줄은 답장해도 그 메시지까지만 읽음.
func TestTUIThreadAndChannelRead(t *testing.T) {
	f, m := filledTUI(t)
	m.rows[0].latest.ThreadTS = "1700000001.000000"
	_, cmd := m.Update(press("m"))
	m.Update(cmd())
	if len(f.reqs["conversations.mark"]) != 0 || !m.rows[0].done {
		t.Errorf("스레드 m: %v", f.reqs["conversations.mark"])
	}
	m.rows[0].done = false
	m.Update(press("r"))
	typeText(m, "ok")
	_, cmd = m.Update(press("enter"))
	m.Update(cmd())
	if len(f.reqs["conversations.mark"]) != 0 || !m.rows[0].done {
		t.Errorf("스레드 답장: %v", f.reqs["conversations.mark"])
	}

	m.rows[0] = tuiRow{ch: core.Channel{ID: "C1GENERAL0", Kind: core.KindPublic}, latest: core.Message{TS: "1700000003.000000"}}
	m.Update(press("r"))
	typeText(m, "ok")
	_, cmd = m.Update(press("enter"))
	m.Update(cmd())
	if mk := f.reqs["conversations.mark"]; len(mk) != 1 || mk[0].Get("ts") != "1700000003.000000" || !m.rows[0].done {
		t.Errorf("채널 답장 읽음: %v", mk)
	}

	// 보내는 사이 온 멘션은 치우지 않는다(mark는 그 전까지만 했다).
	m.rows[0].done = false
	m.Update(press("r"))
	typeText(m, "ok")
	_, cmd = m.Update(press("enter"))
	m.Update(live("C1GENERAL0", "channel", "U1AAAAAAAA", "1700000004.000000", "<@U0MEMEMEME>", ""))
	m.Update(cmd())
	if i := m.rowIndex("C1GENERAL0"); m.rows[i].done {
		t.Error("보내는 사이 온 것까지 치웠다")
	}
}

// TestTUIMerge: R 합치기 — 채우기가 볼 수 있는 줄(일반 DM, thread_broadcast)은 채우기를 따르고, 늦게 반영된 mark는 치운 채로.
func TestTUIMerge(t *testing.T) {
	_, m := filledTUI(t)
	d3 := m.rows[0]
	bc := d3
	bc.ch = core.Channel{ID: "D1DMDMDMDM", Kind: core.KindIM}
	bc.latest.ThreadTS, bc.latest.Subtype = "1700000001.000000", "thread_broadcast"
	d3.done = true
	m.rows = []tuiRow{d3, bc}
	m.Update(press("R"))
	m.Update(filledMsg{chans: m.chans, items: []*unreadItem{{ch: d3.ch, count: 1,
		latest: slack.Message{Msg: slack.Msg{Timestamp: d3.latest.TS, User: "U3HHHHHHHH"}}}}})
	if len(m.rows) != 1 || m.rows[0].ch.ID != "D3UNREADDM" || !m.rows[0].done {
		t.Errorf("합친 줄: %+v", m.rows)
	}
}

// TestTUIRTM: session 프로필의 첫 채우기가 RTM을 띄우고, 나에게 온 것만 push한다.
func TestTUIRTM(t *testing.T) {
	f := setup(t)
	t.Setenv("GSLACK_TOKEN", "xoxc-test")
	t.Setenv("GSLACK_COOKIE", "xoxd-test")
	f.counts = `{"ok":true,"channels":[],"ims":[],"mpims":[]}`
	f.rtm = []string{
		`{"type":"message","channel":"C1GENERAL0","user":"U1AAAAAAAA","text":"멘션 없음","ts":"1700000101.000000"}`,
		`{"type":"message","channel":"D1DMDMDMDM","user":"U1AAAAAAAA","text":"DM","ts":"1700000102.000000"}`,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a := &app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}
	s, err := a.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(ctx, a, s)
	got := make(chan tea.Msg, 10)
	m.push = func(msg tea.Msg) { got <- msg }
	if msg := m.Init()(); msg.(filledMsg).err != nil {
		t.Fatal(msg)
	}
	select {
	case msg := <-got:
		if l, ok := msg.(liveMsg); !ok || l.e.Channel != "D1DMDMDMDM" || l.e.ChannelType != core.KindIM {
			t.Fatalf("첫 push: %#v", msg)
		}
	case <-ctx.Done():
		t.Fatal("DM이 오지 않았다")
	}
	cancel()
	if msg := <-got; msg != (liveDownMsg{}) {
		t.Errorf("취소 뒤: %#v", msg)
	}
}
