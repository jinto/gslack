package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	m.Update(filledMsg{scope: s.chans, items: []*unreadItem{{ch: core.Channel{ID: "C1GENERAL0", Name: "general"}, count: 1,
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
	drain(m, m.Init())
	if len(m.rows) != 3 || m.rows[0].ch.ID != "D3UNREADDM" || m.filling {
		t.Fatalf("rows %v", m.rows)
	}
	return f, m
}

// drain은 Cmd가 낸 메시지를 Update에 넣고, 그 Update가 낸 Cmd를 이어서 — 채우기 두 단계 끝까지.
func drain(m *tuiModel, cmd tea.Cmd) {
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
}

func dms(m *tuiModel) []core.Channel {
	return slices.DeleteFunc(slices.Clone(m.s.chans), func(c core.Channel) bool { return c.Kind != core.KindIM && c.Kind != core.KindMPIM })
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

// live는 나에게 온 실시간 메시지다. 채널 일반 글은 .alert = false로.
func live(ch, typ, user, ts, text, threadTS string) liveMsg {
	return liveMsg{&slackevents.MessageEvent{Channel: ch, ChannelType: typ, User: user, Text: text,
		Message: &slack.Msg{Channel: ch, User: user, Text: text, Timestamp: ts, ThreadTimestamp: threadTS}}, true}
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
	if len(m.rows) != 4 || m.rows[0].ch.ID != "D9NEWDMDM0" || m.rows[0].count != 1 || m.rows[0].name != "@jk" || m.curID() != "D3UNREADDM" {
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
	drain(m, fill)
	ids := []string{}
	for _, r := range m.rows {
		ids = append(ids, r.ch.ID)
	}
	// D9(치움) 사라짐, D1 스레드 줄 남음. D3는 채우기 1 + 채우는 중 1. C1은 채널 단계가 따른다(가짜 history에 멘션이 없어 채널 구역).
	if strings.Join(ids, ",") != "D3UNREADDM,D1DMDMDMDM,C1GENERAL0,G1NEVERREAD" || m.rows[0].count != 2 || m.rows[0].latest.TS != "1700000014.000000" || notes != 5 {
		t.Errorf("R 뒤: %v %+v 알림 %d", ids, m.rows[0], notes)
	}

	// 채우기가 그 메시지를 이미 셌으면 다시 세지 않는다.
	m.Update(press("R"))
	m.Update(live("D3UNREADDM", "im", "U3HHHHHHHH", "1700000015.000000", "x", ""))
	m.Update(filledMsg{scope: dms(m), items: []*unreadItem{{ch: m.rows[0].ch, count: 5, forMe: true,
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
	if _, cmd := m.Update(filledMsg{}); cmd == nil || !m.filling || m.refill {
		t.Error("refill")
	}
	m.Update(filledMsg{})

	// 첫 채우기가 실패해도 watcher를 받았으면 R은 newWatcher(users.conversations)를 다시 타지 않는다.
	m2 := newTUI(m.ctx, m.a, m.s)
	m2.Update(filledMsg{w: &watcher{}, err: errors.New("boom")})
	n := len(f.reqs["users.conversations"])
	_, cmd := m2.Update(press("R"))
	drain(m2, cmd)
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
	m.Update(filledMsg{scope: dms(m), items: []*unreadItem{{ch: d3.ch, count: 1, forMe: true,
		latest: slack.Message{Msg: slack.Msg{Timestamp: d3.latest.TS, User: "U3HHHHHHHH"}}}}})
	if len(m.rows) != 1 || m.rows[0].ch.ID != "D3UNREADDM" || !m.rows[0].done {
		t.Errorf("합친 줄: %+v", m.rows)
	}
}

// TestTUIRTM: session 프로필의 첫 채우기가 RTM을 띄우고, 가입 채널 글(일반)과 나에게 온 것을 alert로 나눠 push한다.
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
	for _, want := range []liveMsg{{&slackevents.MessageEvent{Channel: "C1GENERAL0", ChannelType: core.KindPublic}, false},
		{&slackevents.MessageEvent{Channel: "D1DMDMDMDM", ChannelType: core.KindIM}, true}} {
		select {
		case msg := <-got:
			if l, ok := msg.(liveMsg); !ok || l.e.Channel != want.e.Channel || l.e.ChannelType != want.e.ChannelType || l.alert != want.alert {
				t.Fatalf("push: %#v", msg)
			}
		case <-ctx.Done():
			t.Fatal("오지 않았다:", want.e.Channel)
		}
	}
	cancel()
	if msg := <-got; msg != (liveDownMsg{}) {
		t.Errorf("취소 뒤: %#v", msg)
	}
}

// TestUnreadForMe: 채널의 안 읽은 것 중 나에게 온 것이 있으면 forMe, latest는 그것. count는 전체. w 없으면(unread 명령) 지금 그대로.
func TestUnreadForMe(t *testing.T) {
	setup(t)
	ctx := context.Background()
	s, err := (&app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}).open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w := &watcher{me: "U0MEMEMEME", member: map[string]bool{"C3MENTION0": true, "C4BROADCST": true}}
	for _, c := range []struct {
		ch     string
		w      *watcher
		forMe  bool
		latest string
		count  int
	}{
		{"C3MENTION0", w, true, "1700000005.000000", 3},
		{"C3MENTION0", nil, false, "1700000006.000000", 3},
		{"C4BROADCST", w, true, "1700000004.000000", 2}, // 내 스레드 broadcast(root)
	} {
		it, err := s.unreadOne(ctx, core.Channel{ID: c.ch, Kind: core.KindPublic}, c.w)
		if err != nil || it.forMe != c.forMe || it.latest.Timestamp != c.latest || it.count != c.count {
			t.Errorf("%s w=%v: %+v %v", c.ch, c.w != nil, it, err)
		}
	}
}

func ids(m *tuiModel) string {
	var out []string
	for _, r := range m.rows {
		out = append(out, r.ch.ID)
	}
	return strings.Join(out, ",")
}

func plain(ch, ts string) liveMsg {
	l := live(ch, "channel", "U1AAAAAAAA", ts, "plain", "")
	l.alert = false
	return l
}

// TestTUIChannels: 두 구역 — 정렬, 머리줄, 창 자르기, 실시간 일반 글과 나에게 온 것.
func TestTUIChannels(t *testing.T) {
	_, m := filledTUI(t) // D3(나에게), C1·G1(채널, 멘션 없음)
	if ids(m) != "D3UNREADDM,C1GENERAL0,G1NEVERREAD" || m.rows[1].forMe || m.rows[2].forMe {
		t.Fatalf("구역: %s %+v", ids(m), m.rows)
	}
	v := m.View()
	for _, want := range []string{" 나에게 (1)", " 채널 (2)", "○ #general", "● @지수"} {
		if !strings.Contains(v, want) {
			t.Errorf("%q 없음:\n%s", want, v)
		}
	}
	m.listH, m.cur = 2, 2 // 머리줄 둘이 끼어도 커서 줄이 보인다
	if v := m.View(); !strings.Contains(v, "#never") || strings.Contains(v, "나에게") {
		t.Errorf("창:\n%s", v)
	}
	m.listH, m.cur = 9, 0

	var notes int
	orig := notifyFn
	t.Cleanup(func() { notifyFn = orig })
	notifyFn = func(string, string) error { notes++; return nil }
	m.notify = true
	run := func(_ tea.Model, cmd tea.Cmd) {
		if cmd != nil {
			cmd()
		}
	}
	run(m.Update(plain("C1GENERAL0", "1700000020.000000")))
	if r := at(m, "C1GENERAL0"); r.forMe || r.count != 3 || notes != 0 {
		t.Errorf("일반 글: %+v 알림 %d", r, notes)
	}
	run(m.Update(live("C1GENERAL0", "channel", "U1AAAAAAAA", "1700000021.000000", "<@U0MEMEMEME>", "")))
	if r := m.rows[0]; r.ch.ID != "C1GENERAL0" || !r.forMe || r.count != 4 || notes != 1 {
		t.Errorf("나에게로: %s %+v 알림 %d", ids(m), r, notes)
	}
	m.Update(plain("C1GENERAL0", "1700000022.000000")) // 안 치운 나에게 줄은 멘션을 latest로 둔다
	if r := at(m, "C1GENERAL0"); r.latest.TS != "1700000021.000000" || r.count != 4 {
		t.Errorf("나에게 줄에 일반 글: %+v", r)
	}
	m.rows[0].done = true
	m.Update(plain("C1GENERAL0", "1700000023.000000"))
	if r := at(m, "C1GENERAL0"); r.forMe || r.done || r.count != 1 || ids(m) != "D3UNREADDM,C1GENERAL0,G1NEVERREAD" {
		t.Errorf("치운 뒤 일반 글: %s %+v", ids(m), r)
	}

	m.rows = m.rows[1:] // 나에게 줄이 없어도 머리줄은 보인다
	if v := m.View(); !strings.Contains(v, " 나에게 (0)") {
		t.Errorf("나에게 (0):\n%s", v)
	}
}

// TestTUIMergeScope: 채우기는 자기 scope만 따른다. 나에게 스레드 줄은 더 새 채널 채우기 줄에 지지 않는다.
func TestTUIMergeScope(t *testing.T) {
	_, m := filledTUI(t)
	c1 := m.s.chans[slices.IndexFunc(m.s.chans, func(c core.Channel) bool { return c.ID == "C1GENERAL0" })]
	th := tuiRow{ch: c1, count: 1, forMe: true, latest: core.Message{TS: "1700000030.000000", ThreadTS: "1700000001.000000"}}
	m.rows = []tuiRow{m.rows[0], th, m.rows[2]}
	m.Update(filledMsg{scope: []core.Channel{c1, m.rows[2].ch}, items: []*unreadItem{{ch: c1, count: 9,
		latest: slack.Message{Msg: slack.Msg{Timestamp: "1700000040.000000", User: "U1AAAAAAAA"}}}}})
	if r := at(m, "C1GENERAL0"); !r.forMe || r.latest.TS != "1700000030.000000" || ids(m) != "C1GENERAL0,D3UNREADDM" {
		t.Errorf("합치기: %s %+v", ids(m), r) // G1은 scope 안인데 채우기에 없다 — 읽음
	}
}

func at(m *tuiModel, ch string) tuiRow { return m.rows[m.rowIndex(ch)] }

// TestTUIFillPhases: DM 실패여도 채널 단계, 재연결은 DM만, 채우는 중 R, 상태줄 진행.
func TestTUIFillPhases(t *testing.T) {
	_, m := filledTUI(t)
	m.filling = true
	if _, cmd := m.Update(filledMsg{err: errors.New("boom"), next: true}); cmd == nil || !m.filling {
		t.Error("DM 실패 뒤 채널 단계가 없다")
	}
	if _, cmd := m.Update(press("R")); cmd != nil || !strings.Contains(m.status, "끝나면 R") {
		t.Errorf("채우는 중 R: %q", m.status)
	}
	m.Update(filledMsg{})

	_, cmd := m.Update(reconnectedMsg{})
	if msg := cmd().(filledMsg); msg.next || len(msg.scope) != 2 {
		t.Errorf("재연결은 DM만: next %v scope %v", msg.next, msg.scope)
	}

	got := make(chan tea.Msg, 100)
	m.a.errw = statusWriter(func(msg tea.Msg) { got <- msg })
	m.fillChannels()()
	close(got)
	var last tea.Msg
	progress := false
	for msg := range got {
		progress = progress || strings.Contains(string(msg.(statusMsg)), "확인 중")
		last = msg
	}
	if !progress || last != statusMsg("\r") {
		t.Errorf("진행: %v 마지막 %q", progress, last)
	}
	m.Update(statusMsg("\r확인 중 1/3"))
	if v := m.View(); !strings.Contains(v, "확인 중 1/3") || strings.Contains(v, "채우는 중…") {
		t.Errorf("채우는 중 상태줄:\n%s", v)
	}
	m.Update(statusMsg("전송 여부 불확실"))
	m.Update(statusMsg("\r확인 중 2/3")) // 진행이 경고를 덮지 않는다
	if v := m.View(); !strings.Contains(v, "전송 여부 불확실") {
		t.Errorf("진행이 상태를 덮음:\n%s", v)
	}
	m.Update(last)
	if m.progress != "" || m.status != "전송 여부 불확실" {
		t.Errorf("끝난 진행: %q 상태 %q", m.progress, m.status)
	}
}

// TestTUIScan: scope는 줄이기 전 전체(session은 client.counts가 줄인다), 뮤트 채널은 나에게 온 것이 없으면 뺀다.
func TestTUIScan(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := &app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}
	s, err := a.open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.loadChannels(ctx, false); err != nil {
		t.Fatal(err)
	}
	s.chans = append(s.chans, core.Channel{ID: "C3MENTION0", Kind: core.KindPublic})
	w := &watcher{me: "U0MEMEMEME", member: map[string]bool{"C3MENTION0": true}}
	msg := scan(ctx, a, s, w, map[string]bool{"C1GENERAL0": true, "C3MENTION0": true}, core.KindPublic, core.KindPrivate)
	got := []string{}
	for _, it := range msg.items {
		got = append(got, it.ch.ID)
	}
	slices.Sort(got)
	if len(msg.scope) != 4 || strings.Join(got, ",") != "C3MENTION0,G1NEVERREAD" { // 뮤트여도 멘션은 남는다
		t.Errorf("뮤트: scope %v items %v", msg.scope, got)
	}

	t.Setenv("GSLACK_TOKEN", "xoxc-test")
	t.Setenv("GSLACK_COOKIE", "xoxd-test")
	f.counts = `{"ok":true,"channels":[],"ims":[],"mpims":[]}`
	s, err = (&app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}).open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.loadChannels(ctx, false)
	if msg := scan(ctx, a, s, nil, nil, core.KindPublic, core.KindPrivate); len(msg.scope) != 3 || len(msg.items) != 0 {
		t.Errorf("session: scope %v items %v", msg.scope, msg.items)
	}
}

func TestInbox(t *testing.T) {
	w := &watcher{me: "U0MEMEMEME", all: true, member: map[string]bool{"C1": true, "C2": true}}
	muted := map[string]bool{"C2": true}
	ev := func(ch, text, ts, threadTS, sub string) *slackevents.MessageEvent {
		return &slackevents.MessageEvent{Channel: ch, ChannelType: "channel", User: "U1", Text: text, SubType: sub,
			Message: &slack.Msg{User: "U1", Text: text, Timestamp: ts, ThreadTimestamp: threadTS, SubType: sub}}
	}
	for _, c := range []struct {
		name        string
		e           *slackevents.MessageEvent
		show, alert bool
	}{
		{"일반 글", ev("C1", "hi", "2.0", "", ""), true, false},
		{"뮤트 일반 글", ev("C2", "hi", "2.0", "", ""), false, false},
		{"뮤트 멘션", ev("C2", "<@U0MEMEMEME>", "2.0", "", ""), true, true},
		{"남의 스레드 답글", ev("C1", "hi", "2.0", "1.0", ""), false, false},
		{"broadcast", ev("C1", "hi", "2.0", "1.0", "thread_broadcast"), true, false},
		{"스레드 부모", ev("C1", "hi", "1.0", "1.0", ""), true, false},
		{"가입 안 한 채널", ev("C9", "hi", "2.0", "", ""), false, false},
	} {
		if show, alert := inbox(w, muted, c.e); show != c.show || alert != c.alert {
			t.Errorf("%s: %v %v", c.name, show, alert)
		}
	}
}

func TestMutedChannels(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s, err := (&app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}).open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.prefs = `{"ok":true,"prefs":{"all_notifications_prefs":"{\"channels\":{\"C1\":{\"muted\":true},\"C2\":{\"muted\":false}}}"}}`
	if got, err := s.mutedChannels(ctx); err != nil || len(got) != 1 || !got["C1"] {
		t.Errorf("뮤트: %v %v", got, err)
	}
	f.prefs = `{"ok":true,"prefs":{}}`
	if got, err := s.mutedChannels(ctx); err != nil || len(got) != 0 {
		t.Errorf("설정 없음: %v %v", got, err)
	}
	for _, body := range []string{`{"ok":true,"prefs":{"all_notifications_prefs":"{"}}`, `{"ok":false,"error":"missing_scope"}`} {
		f.prefs = body
		if _, err := s.mutedChannels(ctx); err == nil {
			t.Errorf("%s: 오류가 아니다", body)
		}
	}
}

// TestTUIMergeDoneAcrossPhases: 치운 채널 줄은 DM 단계를 건너 채널 단계까지 가서 늦게 반영된 mark를 가린다.
func TestTUIMergeDoneAcrossPhases(t *testing.T) {
	_, m := filledTUI(t)
	c1 := at(m, "C1GENERAL0")
	m.rows[m.rowIndex("C1GENERAL0")].done = true
	m.filling = true
	m.Update(filledMsg{scope: dms(m), items: []*unreadItem{{ch: m.rows[0].ch, count: 1, forMe: true,
		latest: slack.Message{Msg: slack.Msg{Timestamp: m.rows[0].latest.TS, User: "U3HHHHHHHH"}}}}, next: true})
	m.Update(filledMsg{scope: []core.Channel{c1.ch}, items: []*unreadItem{{ch: c1.ch, count: 2,
		latest: slack.Message{Msg: slack.Msg{Timestamp: c1.latest.TS, User: "U1AAAAAAAA"}}}}})
	if r := at(m, "C1GENERAL0"); !r.done {
		t.Errorf("치운 줄이 되살아남: %+v", r)
	}
}

// TestTUIPreviewFirstUnread: 미리보기는 안 본 첫 메시지를 맨 위에, last_read를 모르면 맨 아래.
func TestTUIPreviewFirstUnread(t *testing.T) {
	f, m := filledTUI(t)
	var msgs []slack.Message
	for i := range 30 {
		msgs = append(msgs, slack.Message{Msg: slack.Msg{Timestamp: fmt.Sprintf("17000000%02d.000000", i), User: "U1AAAAAAAA", Text: fmt.Sprint("m", i)}})
	}
	show := func(read string) string {
		m.Update(previewMsg{seq: m.seq, head: "#general", ch: "C1GENERAL0", msgs: msgs, latest: "1700000029.000000", read: read})
		return strings.Split(m.vp.View(), "\n")[0]
	}
	if top := show("1700000005.000000"); !strings.HasPrefix(top, "┃") || m.vp.YOffset != m.pvTops[6] || m.sel != 6 { // 안 본 게 화면보다 길다
		t.Errorf("안 본 첫 메시지가 맨 위가 아니다: %q sel %d", top, m.sel)
	}
	if show("1700000025.000000"); !m.vp.AtBottom() || m.sel != 26 { // 짧으면 맨 아래, 고른 것은 그대로
		t.Errorf("짧은 안 봄: sel %d", m.sel)
	}
	if show(""); !m.vp.AtBottom() || m.sel != 29 {
		t.Error("last_read 모르면 마지막을 골라 맨 아래")
	}
	n := len(f.reqs["conversations.info"])
	_, cmd := m.Update(press("enter"))
	if msg := cmd().(previewMsg); msg.read != "1700000001.000000" || len(f.reqs["conversations.info"]) != n+1 {
		t.Errorf("last_read: %q", msg.read)
	}
}

// TestTUIFocus: Enter → 미리보기 포커스(구분선에 이름, 힌트, 목록은 작게), j/k는 메시지, 다시 Enter는 무시, Esc로 목록.
func TestTUIFocus(t *testing.T) {
	_, m := filledTUI(t)
	m.liveErr, m.status = nil, "" // 힌트가 80칸에 다 보이게
	if m.listH != 5 {             // 머리줄 둘 + 줄 셋 — 40%(9)보다 작으면 줄 수만큼
		t.Errorf("목록 높이 %d", m.listH)
	}
	_, cmd := m.Update(press("enter"))
	if v := m.View(); !m.focus || !strings.Contains(v, "━━ @지수") || !strings.Contains(v, "Esc 목록") || m.listH > 3 || strings.Count(v, "\n") != 23 {
		t.Fatalf("포커스 %v listH %d:\n%s", m.focus, m.listH, v)
	}
	m.Update(cmd())
	seq := m.seq
	m.Update(press("enter"))
	m.Update(press("j"))
	if m.seq != seq || m.cur != 0 || m.sel != 1 {
		t.Errorf("미리보기 키: seq %d→%d cur %d sel %d", seq, m.seq, m.cur, m.sel)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if v := m.View(); m.focus || m.listH != 5 || strings.Contains(v, "━━") || strings.Count(v, "\n") != 23 {
		t.Errorf("Esc: 포커스 %v listH %d", m.focus, m.listH)
	}
}

// TestTUIPreviewReply: 포커스 중 r·o는 고른 메시지로, 대상은 미리보기한 줄(목록 커서와 무관).
func TestTUIPreviewReply(t *testing.T) {
	f, m := filledTUI(t)
	m.Update(press("j")) // #general(last_read …02, 미리보기 …03 …04)
	_, cmd := m.Update(press("enter"))
	if _, c := m.Update(press("r")); c == nil || m.reply.latest.TS != at(m, "C1GENERAL0").latest.TS { // 응답 전엔 줄 기준
		t.Errorf("응답 전 r: %+v", m.reply)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(cmd())
	if m.sel != 0 || m.pvMsgs[0].TS != "1700000003.000000" {
		t.Fatalf("처음 고른 것: %d %+v", m.sel, m.pvMsgs)
	}
	m.Update(press("j"))
	m.Update(press("k"))
	m.cur = 0 // 실시간 등으로 목록 커서가 다른 줄로 가도
	m.Update(press("r"))
	if !strings.Contains(m.ti.Prompt, "#general ↳ @Deploy Bot: deployed") {
		t.Errorf("입력줄: %q", m.ti.Prompt)
	}
	typeText(m, "ok")
	_, cmd = m.Update(press("enter"))
	_, cmd = m.Update(cmd())
	p, mk := f.reqs["chat.postMessage"], f.reqs["conversations.mark"]
	if len(p) != 1 || p[0].Get("channel") != "C1GENERAL0" || p[0].Get("thread_ts") != "1700000003.000000" ||
		len(mk) != 1 || mk[0].Get("ts") != "1700000004.000000" { // 옛 메시지에 답해도 미리보기로 본 데까지
		t.Errorf("보낸 것: %v 읽음 %v", p, mk)
	}
	if msg := cmd().(previewMsg); msg.head != "#general" || !m.focus {
		t.Errorf("답장 뒤 미리보기: %q 포커스 %v", msg.head, m.focus)
	}

	var got string
	orig := openURL
	t.Cleanup(func() { openURL = orig })
	openURL = func(u string) error { got = u; return nil }
	m.Update(press("j"))
	_, cmd = m.Update(press("o"))
	if cmd(); got != m.pvMsgs[1].Permalink || got == "" {
		t.Errorf("o: %q", got)
	}

	_, cmd = m.Update(press("m"))
	m.Update(cmd())
	if m.focus {
		t.Error("치우면 목록으로")
	}
}

// TestTUIPreviewReplyThread: 스레드 미리보기에서 부모를 골라도 r은 그 스레드로. DM 본문은 대화에 바로.
func TestTUIPreviewReplyThread(t *testing.T) {
	f, m := filledTUI(t)
	_, cmd := m.Update(press("enter")) // DM history
	m.Update(cmd())
	m.Update(press("r"))
	typeText(m, "a")
	_, cmd = m.Update(press("enter"))
	m.Update(cmd())
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	m.rows[0].latest.TS, m.rows[0].latest.ThreadTS = "1700000002.500000", "1700000002.000000"
	_, cmd = m.Update(press("enter"))
	m.Update(cmd())
	m.Update(press("k")) // 부모
	m.Update(press("r"))
	typeText(m, "b")
	_, cmd = m.Update(press("enter"))
	m.Update(cmd())
	if p := f.reqs["chat.postMessage"]; len(p) != 2 || p[0].Get("thread_ts") != "" || p[1].Get("thread_ts") != "1700000002.000000" {
		t.Errorf("보낸 것: %v", p)
	}
}

// TestTUILayoutResize: 화면 높이가 바뀌어도 고른 메시지가 보인다.
func TestTUILayoutResize(t *testing.T) {
	_, m := filledTUI(t)
	var msgs []slack.Message
	for i := range 30 {
		msgs = append(msgs, slack.Message{Msg: slack.Msg{Timestamp: fmt.Sprintf("17000000%02d.000000", i), User: "U1AAAAAAAA", Text: "x"}})
	}
	m.Update(press("enter"))
	m.Update(previewMsg{seq: m.seq, head: "@지수", ch: "D3UNREADDM", msgs: msgs})
	for range 3 {
		m.Update(press("k"))
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	if from, to := m.pvSpan(); from < m.vp.YOffset || to > m.vp.YOffset+m.vp.Height {
		t.Errorf("고른 줄 %d-%d, 화면 %d+%d", from, to, m.vp.YOffset, m.vp.Height)
	}
	for range 30 {
		m.Update(press("k"))
	}
	if from, _ := m.pvSpan(); m.sel != 0 || from < m.vp.YOffset {
		t.Errorf("맨 위로: sel %d 줄 %d 화면 %d", m.sel, from, m.vp.YOffset)
	}
}

// TestTUIPreviewBroadcast: broadcast를 골라 답해도 다시 받는 미리보기는 그 대화(스레드로 바뀌지 않음). 강조는 포커스 따라.
func TestTUIPreviewBroadcast(t *testing.T) {
	_, m := filledTUI(t)
	m.Update(press("j"))
	m.Update(press("enter"))
	m.Update(previewMsg{seq: m.seq, head: "#general", ch: "C1GENERAL0", msgs: []slack.Message{
		{Msg: slack.Msg{Timestamp: "1700000003.000000", User: "U1AAAAAAAA", Text: "bc", SubType: "thread_broadcast", ThreadTimestamp: "1700000001.000000"}}}})
	m.Update(press("r"))
	typeText(m, "ok")
	_, cmd := m.Update(press("enter"))
	if _, cmd = m.Update(cmd()); cmd().(previewMsg).head != "#general" {
		t.Error("답장 뒤 미리보기가 스레드로 바뀜")
	}
	m.Update(press("m"))
	if m.focus {
		t.Error("m은 바로 목록으로")
	}
}

// TestTUIOpenThread: 미리보기에서 답글 달린 메시지·broadcast를 골라 Enter → 그 스레드, Esc → 채널(다시 받지 않음), 다시 Esc → 목록.
func TestTUIOpenThread(t *testing.T) {
	f, m := filledTUI(t)
	m.liveErr, m.status = nil, "" // 상태가 키 힌트를 가린다
	m.cur = m.rowIndex("C1GENERAL0")
	m.Update(press("enter"))
	msg := func(ts, th string, n int) slack.Message {
		return slack.Message{Msg: slack.Msg{Timestamp: "17000000" + ts + ".000000", ThreadTimestamp: th, ReplyCount: n, User: "U1AAAAAAAA", Text: "x" + ts}}
	}
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	general := previewMsg{head: "#general", ch: "C1GENERAL0", latest: "1700000004.000000",
		msgs: []slack.Message{msg("01", "1700000001.000000", 2), msg("03", "1700000001.000000", 0), msg("04", "", 0)}}
	general.seq = m.seq
	m.Update(general)
	seq := m.seq
	if _, cmd := m.Update(press("enter")); cmd != nil || m.seq != seq || strings.Contains(m.View(), "Enter 스레드") {
		t.Fatal("답글 없는 메시지 Enter")
	}
	m.Update(press("k"))
	m.Update(press("k")) // 부모
	if !strings.Contains(m.View(), "Enter 스레드") {
		t.Errorf("힌트: %q", m.View())
	}
	_, cmd := m.Update(press("enter"))
	pm := cmd().(previewMsg)
	if r := f.reqs["conversations.replies"]; r[len(r)-1].Get("ts") != "1700000001.000000" || pm.head != "#general — 스레드" {
		t.Fatalf("스레드: %v %q", r, pm.head)
	}
	m.Update(pm)
	if _, c := m.Update(press("enter")); c != nil || !strings.Contains(m.View(), "Esc 채널") {
		t.Errorf("스레드 안 Enter·힌트: %q", m.View())
	}
	mk := len(f.reqs["conversations.mark"])
	m.Update(press("r"))
	typeText(m, "ok")
	_, cmd = m.Update(press("enter"))
	if _, cmd = m.Update(cmd()); cmd().(previewMsg).head != "#general — 스레드" {
		t.Error("답장 뒤 미리보기는 그 스레드")
	}
	if p := f.reqs["chat.postMessage"]; p[len(p)-1].Get("thread_ts") != "1700000001.000000" || len(f.reqs["conversations.mark"]) != mk {
		t.Errorf("스레드 답장: %v", p)
	}

	n := len(f.reqs["conversations.replies"]) + len(f.reqs["conversations.history"])
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m.Update(esc)
	if from, to := m.pvSpan(); !m.focus || m.back != nil || m.sel != 0 || m.pvRow.thread() || len(m.pvMsgs) != 3 ||
		from < m.vp.YOffset || to > m.vp.YOffset+m.vp.Height || len(f.reqs["conversations.replies"])+len(f.reqs["conversations.history"]) != n {
		t.Fatalf("채널로: focus %v sel %d %d-%d 화면 %d", m.focus, m.sel, from, to, m.vp.YOffset)
	}

	m.Update(press("j")) // broadcast — 받는 중 Esc면 늦은 응답은 버린다
	_, cmd = m.Update(press("enter"))
	m.Update(esc)
	m.Update(cmd())
	if r := f.reqs["conversations.replies"]; r[len(r)-1].Get("ts") != "1700000001.000000" || m.pvRow.thread() || len(m.pvMsgs) != 3 || m.sel != 1 {
		t.Errorf("broadcast·늦은 응답: %v %d", r, m.sel)
	}

	_, cmd = m.Update(press("enter"))
	m.Update(cmd())
	_, cmd = m.Update(press("m")) // 스레드 안 m은 채널을 치운다
	m.Update(cmd())
	if r := f.reqs["conversations.mark"]; len(r) != mk+1 || r[mk].Get("channel") != "C1GENERAL0" || r[mk].Get("ts") != "1700000004.000000" || m.focus || m.back != nil {
		t.Errorf("스레드 안 m: %v focus %v", r, m.focus)
	}

	m.Update(press("enter")) // 스레드 받기 실패해도 Esc로 채널, 다시 Esc로 목록
	general.seq = m.seq
	m.Update(general)
	m.Update(press("k"))
	m.Update(press("enter"))
	m.Update(previewMsg{seq: m.seq, head: "#general — 스레드", err: errors.New("boom")})
	m.Update(esc)
	if !m.focus || len(m.pvMsgs) != 3 {
		t.Errorf("실패 뒤 Esc: focus %v %d", m.focus, len(m.pvMsgs))
	}
	if m.Update(esc); m.focus || m.back != nil {
		t.Error("다시 Esc → 목록")
	}
}
