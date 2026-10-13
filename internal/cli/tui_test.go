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
		latest: slack.Message{Msg: slack.Msg{Timestamp: "1700000002.500000", ThreadTimestamp: "1700000002.000000", User: "U1AAAAAAAA"}}}}})
	_, cmd := m.Update(press("enter"))
	m.Update(cmd())
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
