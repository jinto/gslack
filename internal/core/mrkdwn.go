package core

import (
	"regexp"
	"strings"
)

var (
	tagRe    = regexp.MustCompile(`<([^<>]+)>`)
	entities = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")
)

// Render는 Slack mrkdwn을 터미널용 평문으로 바꾼다. user/channel/group은 ID → 이름(없으면 "").
// 태그를 먼저 나눈 뒤 각 조각의 엔티티를 디코드한다(링크 안의 &amp; 보존).
func Render(text string, user, channel, group func(id string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range tagRe.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(entities.Replace(text[last:m[0]]))
		b.WriteString(renderTag(text[m[2]:m[3]], user, channel, group))
		last = m[1]
	}
	b.WriteString(entities.Replace(text[last:]))
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
