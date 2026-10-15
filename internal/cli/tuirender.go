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
	"github.com/charmbracelet/x/cellbuf"
	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
)

// tui 미리보기 렌더링 — Slack 원본처럼 이름·시각 머리줄, 첨부 색 막대, mrkdwn 스타일, 폭 맞춤 줄바꿈.
// 평문을 만든 뒤 덧입히지 않고 span(글자 + 스타일 + 링크) 단위로 만들고 span 단위로 감싼다 —
// 줄을 넘는 굵게·링크도 줄마다 온전하고, URL·이름 안의 _는 기울임이 되지 않는다.
// 네트워크는 쓰지 않는다(이름 캐시 빼고) — 그림은 Cmd가 받아 둔 것(tuiimage.go)을 찾기만.

const bodyIndent = "       " // "HH:MM  " — 아바타가 들어오면 바뀐다(T7b)

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
	quote bool   // 인용 줄 표시(글자 없음) — 감싼 줄마다 │
	raw   bool   // 이미 칠한 줄(그림) — clean·스타일 없이 그대로
}

// 보호 구간(코드·태그)은 plane 15 사용자 영역 글자로 바꿔 두고 강조 정규식이 건드리지 않게 한다.
// 원문의 같은 영역 글자는 clean이 미리 바꾼다(Nerd Font 아이콘 등은 BMP 영역이라 그대로).
const (
	boldOn, boldOff     = '\U000F0000', '\U000F0001'
	italOn, italOff     = '\U000F0002', '\U000F0003'
	strikeOn, strikeOff = '\U000F0004', '\U000F0005'
	quoteMark           = '\U000F0006'
	slotBase            = '\U000F1000'
)

// clean은 터미널로 나갈 글자에서 제어문자(ESC 등 — 터미널 제어열 주입)와 plane 15 사용자 영역을 지운다. 탭은 공백.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
			return -1
		case r >= 0xF0000 && r <= 0xFFFFD:
			return '\uFFFD'
		}
		return r
	}, s)
}

var (
	codeBlockRe  = regexp.MustCompile("(?s)\n?```(.*?)```\n?") // 앞뒤 줄바꿈을 먹는다 — 블록 앞뒤 빈 줄 없게
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
// ponytail: 마커 종류마다 따로 짝을 지어 교차(*a _b* c_)는 겹친 스타일이 된다 — 드물어서 둔다. 맞추려면 마커 스택 하나.
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
	text = clean(text)
	var slots []span
	protect := func(re *regexp.Regexp, text string, f func(m []string) span) string {
		return re.ReplaceAllStringFunc(text, func(x string) string {
			slots = append(slots, f(re.FindStringSubmatch(x)))
			return string(rune(slotBase + len(slots) - 1))
		})
	}
	text = protect(codeBlockRe, text, func(m []string) span {
		return span{text: "\n" + htmlEntities.Replace(strings.Trim(m[1], "\n")) + "\n", style: codeStyle}
	})
	text = protect(inlineCodeRe, text, func(m []string) span { return span{text: htmlEntities.Replace(m[1]), style: codeStyle} })
	text = protect(mrkTagRe, text, func(m []string) span {
		target, label, _ := strings.Cut(m[1], "|")
		if strings.IndexAny(target, "@#!") == 0 || strings.HasPrefix(target, "mailto:") && label == "" {
			return span{text: s.render(ctx, m[0]), style: base} // 이름·채널·그룹·특수 명령
		}
		target, label = htmlEntities.Replace(target), htmlEntities.Replace(label)
		return span{text: cmp.Or(label, target), style: linkStyle, link: target}
	})
	text = core.Emoji(text) // 코드·태그를 뗀 뒤, 강조 전에(heavy_check_mark의 _)
	text = quoteRe.ReplaceAllString(text, string(quoteMark))
	text = emph(text, '*', boldOn, boldOff)
	text = emph(text, '_', italOn, italOff)
	text = emph(text, '~', strikeOn, strikeOff)

	var out []span
	var bold, ital, strike bool
	style := func(st lipgloss.Style) lipgloss.Style { // 바탕이 굵으면(header) 그대로
		return st.Bold(bold || st.GetBold()).Italic(ital || st.GetItalic()).Strikethrough(strike || st.GetStrikethrough())
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
			sp := slots[r-slotBase]
			out = append(out, span{text: sp.text, style: style(sp.style), link: sp.link})
		case r == quoteMark:
			flush()
			out = append(out, span{quote: true})
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

// renderSpan은 span 하나를 스타일·링크로 감싼다. 여러 줄이면 줄마다(lipgloss는 여러 줄을 가장 긴 줄 폭으로 채운다).
func renderSpan(sp span) string {
	if sp.raw {
		return sp.text
	}
	parts := strings.Split(clean(sp.text), "\n")
	for i, t := range parts {
		if t == "" {
			continue
		}
		parts[i] = sp.style.Render(t)
		if l := safeLink(sp.link); l != "" {
			parts[i] = ansi.SetHyperlink(l) + parts[i] + ansi.ResetHyperlink()
		}
	}
	return strings.Join(parts, "\n")
}

// wrap은 한 줄(줄바꿈 없는 span들)을 width 칸에 감싼다. cellbuf.Wrap은 줄마다 SGR·OSC 8을 다시 연다.
func wrap(spans []span, width int) []string {
	var b strings.Builder
	for _, sp := range spans {
		b.WriteString(renderSpan(sp))
	}
	lines := strings.Split(cellbuf.Wrap(b.String(), max(width, 4), ""), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// splitLines는 span을 논리 줄("\n")로 나누고 앞뒤 빈 줄을 버린다.
func splitLines(spans []span) [][]span {
	lines := [][]span{nil}
	for _, sp := range spans {
		for i, t := range strings.Split(sp.text, "\n") {
			if i > 0 {
				lines = append(lines, nil)
			}
			if t != "" || sp.quote {
				sp.text = t
				lines[len(lines)-1] = append(lines[len(lines)-1], sp)
			}
		}
	}
	blank := func(l []span) bool {
		return !slices.ContainsFunc(l, func(sp span) bool { return strings.TrimSpace(sp.text) != "" || sp.raw && sp.text != "" })
	}
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// prefixed는 논리 줄마다 감싸 접두사를 붙인다. 인용 줄은 감싼 줄마다 │.
func prefixed(prefix string, spans []span, width int) []string {
	var out []string
	for _, line := range splitLines(spans) {
		p := prefix
		if len(line) > 0 && line[0].quote {
			p, line = p+dimStyle.Render("│")+" ", line[1:]
		}
		for _, l := range wrap(line, width-ansi.StringWidth(p)) {
			out = append(out, p+l)
		}
	}
	return out
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
func (s *session) blockSpans(ctx context.Context, blocks []slack.Block, pic picFunc) []span {
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
					// ponytail: context 아이콘은 T7b
				}
			}
			out = append(out, nl(parts)...)
		case *slack.DividerBlock:
			out = append(out, span{text: "───\n", style: dimStyle})
		case *slack.ImageBlock:
			if p := pic(b.ImageURL, 0, 0); p != nil {
				out = append(out, p...)
			} else {
				out = append(out, span{text: "[image: " + cmp.Or(b.AltText, "image") + "]\n", style: dimStyle})
			}
		case *slack.RichTextBlock:
			out = append(out, nl(s.mrkdwnSpans(ctx, richText(b.Elements), lipgloss.NewStyle()))...)
		}
	}
	return out
}

// bodySpans: Slack처럼 rich_text 말고 다른 블록이 있으면 blocks, 아니면 text(rich_text의 text에는 mrkdwn 마커가 그대로 있다).
// cm.Text가 아니라 raw.Text — cm.Text는 비면 첨부 내용으로 채워져 두 번 찍힌다.
func (s *session) bodySpans(ctx context.Context, raw slack.Message, pic picFunc) []span {
	rich := true
	for _, b := range raw.Blocks.BlockSet {
		if _, ok := b.(*slack.RichTextBlock); !ok {
			rich = false
		}
	}
	if !rich || raw.Text == "" {
		if out := s.blockSpans(ctx, raw.Blocks.BlockSet, pic); len(out) > 0 {
			return out
		}
	}
	return s.mrkdwnSpans(ctx, raw.Text, lipgloss.NewStyle())
}

// attachmentLines는 첨부 하나 — pretext는 막대 밖, 나머지는 막대 안.
func (s *session) attachmentLines(ctx context.Context, a slack.Attachment, width int, pic picFunc) []string {
	var lines []string
	if a.Pretext != "" {
		lines = prefixed(bodyIndent, s.mrkdwnSpans(ctx, a.Pretext, lipgloss.NewStyle()), width)
	}
	var in []span
	if src := cmp.Or(a.AuthorName, a.ServiceName); src != "" { // 링크 미리보기(unfurl)는 service_name
		in = append(in, span{text: src + "\n", style: dimStyle.Bold(true)})
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
	in = append(in, s.blockSpans(ctx, a.Blocks.BlockSet, pic)...)
	if len(in) == 0 && a.Pretext == "" && a.Fallback != "" {
		in = nl(s.mrkdwnSpans(ctx, a.Fallback, lipgloss.NewStyle()))
	}
	if p := pic(a.ImageURL, a.ImageWidth, a.ImageHeight); p != nil {
		in = append(in, p...)
	} else if a.ImageURL != "" {
		in = append(in, span{text: "[image]\n", style: dimStyle})
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
	return append(lines, prefixed(bodyIndent+bar, in, width)...)
}

// picFunc는 그림 URL(크기를 알면 w·h)의 자리 span이다 — nil이면 글자 자리표시.
type picFunc func(u string, w, h int) []span

// tuiLines는 메시지 하나의 줄이다(홈 없이 — 홈은 미리보기가 붙인다). width는 홈을 뺀 폭. imgs는 받은 그림(nil이면 글자 자리표시).
func (s *session) tuiLines(ctx context.Context, raw slack.Message, cm core.Message, width int, imgs map[string]pvImage) []string {
	cols := max(min(imgCols, width-len(bodyIndent)-2), 1) // 첨부 막대 2칸까지 뺀다
	pic := func(u string, w, h int) []span { return picture(imgs, u, w, h, cols) }
	head := dimStyle.Render(core.Time(cm.TS).Local().Format("15:04")) + "  " + nameBold.Render(clean(cmp.Or(cm.UserName, cm.User)))
	if cm.Bot {
		head += " " + dimStyle.Render("APP")
	}
	lines := []string{ansi.Truncate(head, width, "…")}
	lines = append(lines, prefixed(bodyIndent, s.bodySpans(ctx, raw, pic), width)...)
	for _, a := range raw.Attachments {
		lines = append(lines, s.attachmentLines(ctx, a, width, pic)...)
	}
	var tags []string
	if cm.ReplyCount > 0 {
		tags = append(tags, fmt.Sprintf("[%d replies]", cm.ReplyCount))
	}
	for _, f := range raw.Files { // 그림이면 썸네일 + 이름, 아니면 제 줄
		sp := []span{{text: "[file: " + f.Name + "]", style: dimStyle}}
		if p := pic(f.Thumb360, f.Thumb360W, f.Thumb360H); p != nil {
			sp = append(p, span{text: f.Name, style: dimStyle})
		}
		lines = append(lines, prefixed(bodyIndent, sp, width)...)
	}
	if cm.Edited {
		tags = append(tags, "(edited)")
	}
	for _, r := range cm.Reactions {
		tags = append(tags, fmt.Sprintf("%s %d", core.Emoji(":"+r.Name+":"), r.Count)) // 이름에 ::skin-tone-N이 붙어 온다
	}
	if len(tags) > 0 {
		lines = append(lines, prefixed(bodyIndent, []span{{text: strings.Join(tags, "  "), style: dimStyle}}, width)...)
	}
	return lines
}
