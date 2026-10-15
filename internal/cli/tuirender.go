package cli

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
)

// tui 미리보기 렌더링 — Slack 원본처럼 이름·시각 머리줄, 첨부 색 막대, mrkdwn 스타일, 폭 맞춤 줄바꿈.
// 평문을 만든 뒤 덧입히지 않고 span(글자 + 스타일 + 링크) 단위로 만들고 span 단위로 감싼다 —
// 줄을 넘는 굵게·링크도 줄마다 온전하고, URL·이름 안의 _는 기울임이 되지 않는다.
// 네트워크는 쓰지 않는다(이름 캐시 빼고) — T7 이미지는 Cmd에서 받는다.

const bodyIndent = "       " // "HH:MM  " — 아바타가 들어오면 바뀐다

var (
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // ANSI 16색 — 테마가 맞춘다(AdaptiveColor는 배경을 묻는다)
	linkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Underline(true)
	codeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	nameBold  = lipgloss.NewStyle().Bold(true)
)

type span struct {
	text  string
	style lipgloss.Style
	link  string // 하이퍼링크(OSC 8) — 안전한 것만
}

// 보호 구간(코드·태그)은 사용자 영역 글자로 바꿔 두고 강조 정규식이 건드리지 않게 한다.
const (
	boldOn, boldOff     = '', ''
	italOn, italOff     = '', ''
	strikeOn, strikeOff = '', ''
	quoteMark           = ''
	slotBase            = ''
)

var (
	codeBlockRe  = regexp.MustCompile("(?s)```(.*?)```")
	inlineCodeRe = regexp.MustCompile("`([^`\n]+)`")
	mrkTagRe     = regexp.MustCompile(`<([^<>\n]+)>`)
	quoteRe      = regexp.MustCompile(`(?m)^(?:&gt;|>) ?`)
	htmlEntities = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")
	emphRes      = map[byte]*regexp.Regexp{}
)

func init() {
	for _, c := range []byte("*_~") {
		q := regexp.QuoteMeta(string(c))
		// 여는 마커 앞은 처음·공백·문장부호(Go 정규식에 lookbehind가 없어 글자를 잡고 되살린다), 안쪽은 공백으로 시작·끝나지 않는다.
		emphRes[c] = regexp.MustCompile(`(^|[^\pL\pN` + q + `])` + q + `([^\s` + q + `](?:[^` + q + `\n]*[^\s` + q + `])?)` + q)
	}
}

// emph는 마커 쌍을 켜고 끄는 글자로 바꾼다. 닫는 마커 뒤가 글자·숫자면(2*3*4, snake_case) 두지 않는다.
func emph(s string, c byte, on, off rune) string {
	var b strings.Builder
	last := 0
	for _, m := range emphRes[c].FindAllStringSubmatchIndex(s, -1) {
		if r := []rune(s[m[1]:]); len(r) > 0 && (unicode.IsLetter(r[0]) || unicode.IsNumber(r[0])) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(s[m[2]:m[3]])
		b.WriteRune(on)
		b.WriteString(s[m[4]:m[5]])
		b.WriteRune(off)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// mrkdwnSpans는 mrkdwn 원문을 span으로 바꾼다. base는 바탕 스타일(context는 흐리게 등).
func (s *session) mrkdwnSpans(ctx context.Context, text string, base lipgloss.Style) []span {
	var slots [][]span
	protect := func(re *regexp.Regexp, text string, f func(m []string) []span) string {
		return re.ReplaceAllStringFunc(text, func(x string) string {
			slots = append(slots, f(re.FindStringSubmatch(x)))
			return string(rune(slotBase + len(slots) - 1))
		})
	}
	text = protect(codeBlockRe, text, func(m []string) []span {
		return []span{{text: "\n" + htmlEntities.Replace(strings.Trim(m[1], "\n")) + "\n", style: codeStyle}}
	})
	text = protect(inlineCodeRe, text, func(m []string) []span { return []span{{text: htmlEntities.Replace(m[1]), style: codeStyle}} })
	text = protect(mrkTagRe, text, func(m []string) []span {
		target, label, _ := strings.Cut(m[1], "|")
		if strings.ContainsAny(target[:1], "@#!") || strings.HasPrefix(target, "mailto:") && label == "" {
			return []span{{text: s.render(ctx, m[0]), style: base}} // 이름·채널·그룹·특수 명령
		}
		target, label = htmlEntities.Replace(target), htmlEntities.Replace(label)
		return []span{{text: cmp.Or(label, target), style: linkStyle, link: target}}
	})
	text = quoteRe.ReplaceAllString(text, string(quoteMark))
	text = emph(text, '*', boldOn, boldOff)
	text = emph(text, '_', italOn, italOff)
	text = emph(text, '~', strikeOn, strikeOff)

	var out []span
	var bold, ital, strike bool
	style := func(st lipgloss.Style) lipgloss.Style {
		if bold {
			st = st.Bold(true)
		}
		if ital {
			st = st.Italic(true)
		}
		if strike {
			st = st.Strikethrough(true)
		}
		return st
	}
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, span{text: htmlEntities.Replace(buf.String()), style: style(base)})
			buf.Reset()
		}
	}
	for _, r := range text {
		switch {
		case r >= slotBase && int(r-slotBase) < len(slots):
			flush()
			for _, sp := range slots[r-slotBase] {
				out = append(out, span{text: sp.text, style: style(sp.style), link: sp.link})
			}
		case r == quoteMark:
			flush()
			out = append(out, span{text: "│ ", style: dimStyle})
		case r >= boldOn && r <= strikeOff:
			flush()
			switch r {
			case boldOn, boldOff:
				bold = r == boldOn
			case italOn, italOff:
				ital = r == italOn
			case strikeOn, strikeOff:
				strike = r == strikeOn
			}
		default:
			buf.WriteRune(r)
		}
	}
	flush()
	return out
}

// safeLink는 OSC 8에 넣어도 되는 URL이다 — 제어문자(터미널 제어열 주입)와 http·https·mailto 밖은 버린다.
func safeLink(u string) string {
	if strings.IndexFunc(u, func(r rune) bool { return r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f }) >= 0 {
		return ""
	}
	if l := strings.ToLower(u); strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "mailto:") {
		return u
	}
	return ""
}

var wordRe = regexp.MustCompile(`\s+|\S+`)

// wrap은 span을 단어 단위로 width 칸에 채워 줄마다 렌더링한다. "\n"은 강제 줄바꿈, 한 단어가 폭보다 길면 글자로 자른다.
// 공백은 다음 단어가 같은 줄에 들어갈 때만 쓴다(줄 끝·감싼 줄 앞 공백 없음).
func wrap(spans []span, width int) []string {
	width = max(width, 4)
	var lines []string
	var cur strings.Builder
	w := 0
	var gap *span // 아직 안 쓴 공백
	put := func(sp span, text string) {
		out := sp.style.Render(text)
		if l := safeLink(sp.link); l != "" {
			out = ansi.SetHyperlink(l) + out + ansi.ResetHyperlink()
		}
		cur.WriteString(out)
		w += ansi.StringWidth(text)
	}
	newline := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		w, gap = 0, nil
	}
	for _, sp := range spans {
		for i, part := range strings.Split(sp.text, "\n") {
			if i > 0 {
				newline()
			}
			for _, tok := range wordRe.FindAllString(part, -1) {
				if strings.TrimSpace(tok) == "" {
					if w > 0 || len(lines) == 0 || i > 0 { // 감싼 줄 앞 공백은 버린다(강제 줄바꿈 뒤 들여쓰기는 둔다)
						g := sp
						g.text = tok
						gap = &g
					}
					continue
				}
				gw := 0
				if gap != nil {
					gw = ansi.StringWidth(gap.text)
				}
				tw := ansi.StringWidth(tok)
				if w > 0 && w+gw+tw > width {
					newline()
				}
				if gap != nil {
					put(*gap, gap.text)
					gap = nil
				}
				if w+tw <= width {
					put(sp, tok)
					continue
				}
				for _, r := range tok { // 폭보다 긴 단어는 글자로
					if rw := ansi.StringWidth(string(r)); w > 0 && w+rw > width {
						newline()
					}
					put(sp, string(r))
				}
			}
		}
	}
	if cur.Len() > 0 || len(lines) == 0 {
		newline()
	}
	return lines
}

// prefixed는 span을 감싸 줄마다 접두사를 붙인다. 접두사 폭은 ansi.StringWidth로 뺀다.
func prefixed(prefix string, spans []span, width int) []string {
	lines := wrap(spans, width-ansi.StringWidth(prefix))
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return lines
}

// barColor는 첨부 color를 막대 색으로. 6자리 hex·good/warning/danger, 그 밖은 회색.
func barColor(c string) lipgloss.TerminalColor {
	c = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c), "#"))
	switch c {
	case "good":
		return lipgloss.Color("#2eb886")
	case "warning":
		return lipgloss.Color("#daa038")
	case "danger":
		return lipgloss.Color("#a30200")
	}
	if _, err := strconv.ParseUint(c, 16, 32); err == nil && len(c) == 6 {
		return lipgloss.Color("#" + c)
	}
	return lipgloss.Color("8")
}

// textSpans: mrkdwn이면 스타일, plain_text면 그대로.
func (s *session) textSpans(ctx context.Context, t *slack.TextBlockObject, base lipgloss.Style) []span {
	if t == nil || t.Text == "" {
		return nil
	}
	if t.Type == slack.PlainTextType {
		return []span{{text: t.Text, style: base}}
	}
	return s.mrkdwnSpans(ctx, t.Text, base)
}

func nl(spans []span) []span {
	if len(spans) == 0 {
		return nil
	}
	return append(spans, span{text: "\n"})
}

// blockSpans는 블록을 span으로(블록 사이 줄바꿈). 모르는 블록(actions·input)은 건너뛴다.
func (s *session) blockSpans(ctx context.Context, blocks []slack.Block) []span {
	var out []span
	for _, b := range blocks {
		switch b := b.(type) {
		case *slack.HeaderBlock:
			out = append(out, nl(s.textSpans(ctx, b.Text, nameBold))...)
		case *slack.SectionBlock:
			out = append(out, nl(s.textSpans(ctx, b.Text, lipgloss.NewStyle()))...)
			for _, f := range b.Fields {
				out = append(out, nl(s.textSpans(ctx, f, lipgloss.NewStyle()))...)
			}
		case *slack.ContextBlock:
			var parts []span
			for _, e := range b.ContextElements.Elements {
				switch e := e.(type) {
				case *slack.TextBlockObject:
					parts = append(parts, s.textSpans(ctx, e, dimStyle)...)
					parts = append(parts, span{text: "  ", style: dimStyle})
				case *slack.ImageBlockElement:
					// ponytail: context 아이콘은 T7(이미지)
				}
			}
			out = append(out, nl(parts)...)
		case *slack.DividerBlock:
			out = append(out, span{text: "───\n", style: dimStyle})
		case *slack.ImageBlock:
			out = append(out, span{text: "[image: " + cmp.Or(b.AltText, "image") + "]\n", style: dimStyle}) // T7
		case *slack.RichTextBlock:
			out = append(out, nl(s.mrkdwnSpans(ctx, richText(b.Elements), lipgloss.NewStyle()))...)
		}
	}
	return out
}

// bodySpans: Slack처럼 rich_text 말고 다른 블록이 있으면 blocks, 아니면 text(rich_text의 text에는 mrkdwn 마커가 그대로 있다).
// cm.Text가 아니라 raw.Text — cm.Text는 비면 첨부 내용으로 채워져 두 번 찍힌다.
func (s *session) bodySpans(ctx context.Context, raw slack.Message) []span {
	rich := true
	for _, b := range raw.Blocks.BlockSet {
		if _, ok := b.(*slack.RichTextBlock); !ok {
			rich = false
		}
	}
	if !rich || raw.Text == "" {
		if out := s.blockSpans(ctx, raw.Blocks.BlockSet); len(out) > 0 {
			return out
		}
	}
	return s.mrkdwnSpans(ctx, raw.Text, lipgloss.NewStyle())
}

// attachmentLines는 첨부 하나 — pretext는 막대 밖, 나머지는 막대 안.
func (s *session) attachmentLines(ctx context.Context, a slack.Attachment, width int) []string {
	var lines []string
	if a.Pretext != "" {
		lines = prefixed(bodyIndent, s.mrkdwnSpans(ctx, a.Pretext, lipgloss.NewStyle()), width)
	}
	var in []span
	if a.AuthorName != "" {
		in = append(in, span{text: a.AuthorName + "\n", style: dimStyle.Bold(true)})
	}
	if a.Title != "" {
		st := nameBold
		if safeLink(a.TitleLink) != "" {
			st = linkStyle.Bold(true)
		}
		in = append(in, span{text: a.Title, style: st, link: a.TitleLink}, span{text: "\n"})
	}
	in = append(in, nl(s.mrkdwnSpans(ctx, a.Text, lipgloss.NewStyle()))...)
	for _, f := range a.Fields {
		in = append(in, span{text: f.Title + "\n", style: nameBold})
		in = append(in, nl(s.mrkdwnSpans(ctx, f.Value, lipgloss.NewStyle()))...)
	}
	in = append(in, s.blockSpans(ctx, a.Blocks.BlockSet)...)
	if len(in) == 0 && a.Pretext == "" && a.Fallback != "" {
		in = nl(s.mrkdwnSpans(ctx, a.Fallback, lipgloss.NewStyle()))
	}
	if a.ImageURL != "" {
		in = append(in, span{text: "[image]\n", style: dimStyle}) // T7
	}
	if foot := a.Footer; foot != "" || a.Ts != "" {
		if f, err := strconv.ParseFloat(string(a.Ts), 64); err == nil && f > 0 {
			foot = strings.TrimPrefix(foot+" · "+time.Unix(int64(f), 0).Local().Format("01/02 15:04"), " · ")
		}
		in = append(in, span{text: foot, style: dimStyle})
	}
	if len(in) == 0 {
		return lines
	}
	bar := "  "
	if !a.HideColor {
		bar = lipgloss.NewStyle().Foreground(barColor(a.Color)).Render("▌") + " "
	}
	return append(lines, prefixed(bodyIndent+bar, trimNL(in), width)...)
}

func trimNL(spans []span) []span {
	for len(spans) > 0 && spans[len(spans)-1].text == "\n" {
		spans = spans[:len(spans)-1]
	}
	if n := len(spans); n > 0 {
		spans[n-1].text = strings.TrimRight(spans[n-1].text, "\n")
	}
	return spans
}

// tuiLines는 메시지 하나의 줄이다(홈 없이 — 홈은 미리보기가 붙인다). width는 홈을 뺀 폭.
func (s *session) tuiLines(ctx context.Context, raw slack.Message, cm core.Message, width int) []string {
	head := dimStyle.Render(core.Time(cm.TS).Local().Format("15:04")) + "  " + nameBold.Render(cmp.Or(cm.UserName, cm.User))
	if cm.Bot {
		head += " " + dimStyle.Render("APP")
	}
	lines := []string{ansi.Truncate(head, width, "…")}
	if body := trimNL(s.bodySpans(ctx, raw)); slices.ContainsFunc(body, func(sp span) bool { return strings.TrimSpace(sp.text) != "" }) {
		lines = append(lines, prefixed(bodyIndent, body, width)...)
	}
	for _, a := range raw.Attachments {
		lines = append(lines, s.attachmentLines(ctx, a, width)...)
	}
	var tags []string
	if cm.ReplyCount > 0 {
		tags = append(tags, fmt.Sprintf("[%d replies]", cm.ReplyCount))
	}
	for _, f := range cm.Files {
		tags = append(tags, "[file: "+f.Name+"]") // T7 썸네일
	}
	if cm.Edited {
		tags = append(tags, "(edited)")
	}
	for _, r := range cm.Reactions {
		tags = append(tags, fmt.Sprintf(":%s: %d", r.Name, r.Count))
	}
	if len(tags) > 0 {
		lines = append(lines, prefixed(bodyIndent, []span{{text: strings.Join(tags, "  "), style: dimStyle}}, width)...)
	}
	return lines
}
