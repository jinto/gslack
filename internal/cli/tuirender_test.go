package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/slack-go/slack"
)

func renderSession(t *testing.T) *session {
	setup(t)
	s, err := (&app{out: &bytes.Buffer{}, errw: &bytes.Buffer{}}).open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.loadChannels(context.Background(), false); err != nil { // #채널 이름
		t.Fatal(err)
	}
	return s
}

// trueColor는 테스트 동안 색을 켠다(lipgloss 프로필은 전역 — 병렬 없음).
func trueColor(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
}

// desc는 span을 [b]…[/b] 꼴로 적는다.
func desc(spans []span) string {
	var b strings.Builder
	for _, sp := range spans {
		t := sp.text
		switch {
		case sp.quote:
			t = "│ "
		case sp.link != "":
			t = "[link " + sp.link + "]" + t + "[/link]"
		case sp.style.GetForeground() == codeStyle.GetForeground():
			t = "[code]" + t + "[/code]"
		}
		if sp.style.GetStrikethrough() {
			t = "[s]" + t + "[/s]"
		}
		if sp.style.GetItalic() {
			t = "[i]" + t + "[/i]"
		}
		if sp.style.GetBold() {
			t = "[b]" + t + "[/b]"
		}
		b.WriteString(t)
	}
	return b.String()
}

func TestMrkdwnSpans(t *testing.T) {
	s := renderSession(t)
	ctx := context.Background()
	for _, c := range []struct{ in, want string }{
		{"*a* and _b_ ~c~ `d`", "[b]a[/b] and [i]b[/i] [s]c[/s] [code]d[/code]"},
		{"*a* *b*", "[b]a[/b] [b]b[/b]"},
		{"2*3*4 snake_case_name a * b", "2*3*4 snake_case_name a * b"},
		{"```x *y* <@U1AAAAAAAA>```", "[code]\nx *y* <@U1AAAAAAAA>\n[/code]"},
		{"<https://x.com/a_b_c|label> <https://y.com>", "[link https://x.com/a_b_c]label[/link] [link https://y.com]https://y.com[/link]"},
		{"hi <@U1AAAAAAAA> &amp; <#C1GENERAL0>", "hi @jk & #general"},
		{"&gt; quoted\nnext", "│ quoted\nnext"},
		{"*see <https://x.com|this>*", "[b]see [/b][b][link https://x.com]this[/link][/b]"},
		{"*unclosed and _also", "*unclosed and _also"},
		{"a <|x> b", "a x b"},                              // 빈 대상 태그 — 패닉 없이
		{"nerd \ue0a0 x", "nerd \ue0a0 x"},                 // BMP 사용자 영역(Nerd Font) 그대로
		{"evil \U000F0000 x \x1b[2J", "evil \ufffd x [2J"}, // 마커 영역·제어문자는 지운다
	} {
		if got := desc(s.mrkdwnSpans(ctx, c.in, lipgloss.NewStyle())); got != c.want {
			t.Errorf("%q\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestWrapKeepsStyle(t *testing.T) {
	trueColor(t)
	lines := wrap([]span{{text: strings.Repeat("굵은 글자 ", 10), style: lipgloss.NewStyle().Bold(true), link: "https://x.com"}}, 20)
	if len(lines) < 3 {
		t.Fatalf("감싸지 않음: %q", lines)
	}
	for _, l := range lines {
		if !strings.Contains(l, "\x1b[1m") || !strings.Contains(l, "\x1b]8;;https://x.com") || ansi.StringWidth(l) > 20 {
			t.Errorf("줄마다 굵게·링크, 폭 ≤ 20: %q", l)
		}
	}
	for _, u := range []string{"javascript:alert(1)", "https://x.com/\x1b]8;;evil\x07", "https://x.com/\u009b"} {
		if l := wrap([]span{{text: "x", link: u}}, 20)[0]; strings.Contains(l, "\x1b]8") {
			t.Errorf("%q: 하이퍼링크가 붙음", u)
		}
	}
}

func grafana(t *testing.T) slack.Message {
	var m slack.Message
	err := json.Unmarshal([]byte(`{"ts":"1792031145.467669","bot_id":"B1","bot_profile":{"name":"LXP Deploy BOT"},"text":"",
		"attachments":[{"color":"#2EB886","title":"[RESOLVED] TDEA PROD Error Spike Detection","title_link":"https://grafana.example.com/alert",
		"text":"*TDEA PROD Error Spike Detection*\nlearning-agent error spike (2.9x baseline)\nService learning-agent has more errors in the last hour than its 7 day hourly baseline. Current: 4 / baseline: 0.4.",
		"footer":"Grafana v13.0.1+security-01","ts":1792031145}]}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTUILinesAttachment(t *testing.T) {
	s := renderSession(t)
	ctx := context.Background()
	raw := grafana(t)
	cm := s.message(ctx, raw, "C1GENERAL0") // cm.Text는 첨부 내용으로 채워진다 — 두 번 찍히면 안 된다
	lines := s.tuiLines(ctx, raw, cm, 60)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"LXP Deploy BOT APP", "▌ [RESOLVED] TDEA PROD Error Spike Detection", "▌ TDEA PROD Error Spike Detection", "▌ Grafana v13.0.1+security-01 · "} {
		if !strings.Contains(plain, want) {
			t.Errorf("%q 없음:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "*") || strings.Count(plain, "learning-agent error spike") != 1 {
		t.Errorf("별표·두 번:\n%s", plain)
	}
	for _, l := range lines {
		if ansi.StringWidth(l) > 60 || strings.HasSuffix(ansi.Strip(l), " ") {
			t.Errorf("폭 넘음·줄 끝 공백: %q", ansi.Strip(l))
		}
	}

	trueColor(t)
	joined := strings.Join(s.tuiLines(ctx, raw, cm, 60), "\n")
	if !strings.Contains(joined, "\x1b]8;;https://grafana.example.com/alert") || !strings.Contains(joined, "38;2;46;184;134") {
		t.Errorf("링크·막대 색 없음: %q", joined)
	}

	raw.Attachments = []slack.Attachment{{Fallback: "only fallback", HideColor: true}}
	if plain := ansi.Strip(strings.Join(s.tuiLines(ctx, raw, cm, 60), "\n")); !strings.Contains(plain, "only fallback") || strings.Contains(plain, "▌") {
		t.Errorf("fallback·HideColor:\n%s", plain)
	}
}

func TestTUILinesBlocks(t *testing.T) {
	s := renderSession(t)
	ctx := context.Background()
	trueColor(t)
	var rich, header slack.Message
	json.Unmarshal([]byte(`{"ts":"1700000001.000000","user":"U1AAAAAAAA","text":"*hi* there",
		"blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"hi there"}]}]}]}`), &rich)
	json.Unmarshal([]byte(`{"ts":"1700000001.000000","bot_id":"B1","text":"fallback text",
		"blocks":[{"type":"header","text":{"type":"plain_text","text":"Deploy"}},{"type":"divider"},
		{"type":"section","text":{"type":"mrkdwn","text":"done *v1*"}},{"type":"context","elements":[{"type":"mrkdwn","text":"by ci"}]}]}`), &header)
	if l := s.tuiLines(ctx, rich, s.message(ctx, rich, "C1GENERAL0"), 60); len(l) != 2 || !strings.Contains(l[1], "\x1b[1mhi") {
		t.Errorf("rich_text + text → text의 굵게: %q", l)
	}
	l := s.tuiLines(ctx, header, s.message(ctx, header, "C1GENERAL0"), 60)
	got := []string{}
	for _, x := range l[1:] {
		got = append(got, strings.TrimSpace(ansi.Strip(x)))
	}
	if strings.Join(got, "|") != "Deploy|───|done v1|by ci" || strings.Contains(strings.Join(l, ""), "fallback text") {
		t.Errorf("blocks: %q", got)
	}
}

// TestTUIPreviewResize: 폭이 바뀌면 다시 그려 줄이 폭 안에, 고른 메시지는 그대로.
func TestTUIPreviewResize(t *testing.T) {
	_, m := filledTUI(t)
	m.Update(press("enter"))
	m.Update(previewMsg{seq: m.seq, head: "#general", ch: "C1GENERAL0", msgs: []slack.Message{grafana(t), grafana(t)}})
	m.Update(press("k"))
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	for _, l := range strings.Split(m.vp.View(), "\n") {
		if ansi.StringWidth(l) > 40 {
			t.Errorf("폭 넘음: %q", l)
		}
	}
	if from, to := m.pvSpan(); m.sel != 0 || from < m.vp.YOffset || from >= m.vp.YOffset+m.vp.Height || to <= from {
		t.Errorf("고른 것 sel %d %d-%d 화면 %d", m.sel, from, to, m.vp.YOffset)
	}
}

// TestTUILinesLayout: 인용은 감싼 줄마다 │, 코드 블록 앞뒤 빈 줄 없음, 파일은 제 줄, 제어문자 없음, unfurl 출처.
func TestTUILinesLayout(t *testing.T) {
	s := renderSession(t)
	ctx := context.Background()
	var raw slack.Message
	json.Unmarshal([]byte(`{"ts":"1700000001.000000","user":"U1AAAAAAAA",
		"text":"&gt; one two three four five six seven eight nine ten\n`+"```"+`code`+"```"+`\nafter",
		"files":[{"id":"F1","name":"a.png"}],"reactions":[{"name":"eyes","count":2}],
		"attachments":[{"service_name":"GitHub","title":"PR \u001b]52;c;x\u0007","title_link":"https://github.com/x","image_url":"https://i/x.png"}]}`), &raw)
	lines := s.tuiLines(ctx, raw, s.message(ctx, raw, "C1GENERAL0"), 30)
	var got []string
	for _, l := range lines[1:] {
		got = append(got, strings.TrimSpace(ansi.Strip(l)))
	}
	want := []string{"│ one two three four", "│ five six seven eight", "│ nine ten", "code", "after",
		"▌ GitHub", "▌ PR ]52;c;x", "▌ [image]", "[file: a.png]", ":eyes: 2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("줄:\n got %q\nwant %q", got, want)
	}
	if j := strings.Join(lines, ""); strings.Contains(j, "\x1b]52") {
		t.Errorf("제어문자가 남음: %q", j)
	}
}
