package core

import (
	"regexp"
	"strings"
)

//go:generate uv run python -I ../../scripts/emoji.py emoji_table.go
//go:generate gofmt -w emoji_table.go

var (
	tagRe    = regexp.MustCompile(`<([^<>]+)>`)
	emojiRe  = regexp.MustCompile(`^:([a-z0-9_+'-]+):(?::(skin-tone-[2-6]):)?`)
	entities = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")
)

// Render는 Slack mrkdwn을 터미널용 평문으로 바꾼다. user/channel/group은 ID → 이름(없으면 "").
// 태그를 먼저 나눈 뒤 각 조각의 엔티티를 디코드한다(링크 안의 &amp; 보존).
func Render(text string, user, channel, group func(id string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range tagRe.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(Emoji(entities.Replace(text[last:m[0]])))
		b.WriteString(renderTag(text[m[2]:m[3]], user, channel, group))
		last = m[1]
	}
	b.WriteString(Emoji(entities.Replace(text[last:])))
	return b.String()
}

// Emoji는 :이름:(뒤에 :skin-tone-N:이 붙을 수 있다)을 글자로 바꾼다. 표에 없는 이름(커스텀 이모지)은 그대로.
// 콜론마다 맞춰 보고 빗나가면 콜론 하나만 넘긴다 — 정규식 치환이면 빗나간 이름이 다음 이름의 콜론을 먹는다.
func Emoji(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, ':')
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		s = s[i:]
		m := emojiRe.FindStringSubmatch(s)
		e, ok := "", false
		if m != nil {
			if e, ok = emojiTable[m[1]+"::"+m[2]]; !ok { // 피부색 변형이 없으면 기본 글자(수식자는 버린다)
				e, ok = emojiTable[m[1]]
			}
		}
		if !ok {
			b.WriteByte(':')
			s = s[1:]
			continue
		}
		b.WriteString(e)
		s = s[len(m[0]):]
	}
	b.WriteString(s)
	return b.String()
}

func renderTag(tag string, user, channel, group func(string) string) string {
	target, label, _ := strings.Cut(tag, "|")
	label = entities.Replace(label)
	switch {
	case strings.HasPrefix(target, "@"):
		if label != "" {
			return "@" + strings.TrimPrefix(label, "@")
		}
		return "@" + or(user(target[1:]), target[1:])
	case strings.HasPrefix(target, "#"):
		if label != "" {
			return "#" + label
		}
		return "#" + or(channel(target[1:]), target[1:])
	case target == "!here", target == "!channel", target == "!everyone":
		return "@" + target[1:]
	case strings.HasPrefix(target, "!subteam^"):
		if label == "" {
			label = or(group(strings.TrimPrefix(target, "!subteam^")), "subteam")
		}
		return "@" + strings.TrimPrefix(label, "@")
	case strings.HasPrefix(target, "!"): // !date^…, 기타 특수 명령
		return or(label, target)
	case strings.HasPrefix(target, "mailto:"):
		return or(label, strings.TrimPrefix(target, "mailto:"))
	}
	target = entities.Replace(target)
	if label == "" || label == target {
		return target
	}
	return label + " (" + target + ")"
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
