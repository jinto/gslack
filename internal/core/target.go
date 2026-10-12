package core

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type TargetKind int

const (
	TargetChannelName TargetKind = iota + 1 // #general, general
	TargetUserName                          // @jaekwon
	TargetChannelID                         // C…/G…/D…
	TargetUserID                            // U…/W…
	TargetPermalink                         // https://x.slack.com/archives/C…/p…
)

type Target struct {
	Kind     TargetKind
	Name     string // ChannelName, UserName
	ID       string // ChannelID, UserID, Permalink(채널)
	TS       string // Permalink
	ThreadTS string // Permalink(스레드 답글이면 부모 ts)
}

// Slack은 ID 길이를 보장하지 않는다 — 하한만 둔다.
var (
	channelIDRe = regexp.MustCompile(`^[CGD][A-Z0-9]{8,}$`)
	userIDRe    = regexp.MustCompile(`^[UW][A-Z0-9]{8,}$`)
	permaTSRe   = regexp.MustCompile(`^p(\d{16})$`)     // 초 10자리 + 마이크로초 6자리
	TSRe        = regexp.MustCompile(`^\d{10}\.\d{6}$`) // Slack ts(초 10자리.마이크로초 6자리)
)

// ParseTarget은 명령 인자를 해석한다. 이름 → ID 해석은 호출자(캐시)가 한다.
func ParseTarget(s string) (Target, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || s == "#" || s == "@":
		return Target{}, fmt.Errorf("대상이 비었다")
	case strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://"):
		return parsePermalink(s)
	case strings.HasPrefix(s, "#"):
		return Target{Kind: TargetChannelName, Name: s[1:]}, nil
	case strings.HasPrefix(s, "@"):
		if userIDRe.MatchString(s[1:]) {
			return Target{Kind: TargetUserID, ID: s[1:]}, nil
		}
		return Target{Kind: TargetUserName, Name: s[1:]}, nil
	case channelIDRe.MatchString(s):
		return Target{Kind: TargetChannelID, ID: s}, nil
	case userIDRe.MatchString(s):
		return Target{Kind: TargetUserID, ID: s}, nil
	}
	// 접두사 없는 단어는 채널 이름 — bash에서 `#general`은 주석이 된다.
	return Target{Kind: TargetChannelName, Name: s}, nil
}

func parsePermalink(s string) (Target, error) {
	u, err := url.Parse(s)
	if err != nil {
		return Target{}, fmt.Errorf("permalink를 해석할 수 없다: %w", err)
	}
	host := u.Hostname()
	if host != "slack.com" && !strings.HasSuffix(host, ".slack.com") {
		return Target{}, fmt.Errorf("Slack permalink가 아니다: %s", host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "archives" || !channelIDRe.MatchString(parts[1]) {
		return Target{}, fmt.Errorf("permalink 형식이 아니다(/archives/<채널>/p<ts>): %s", s)
	}
	m := permaTSRe.FindStringSubmatch(parts[2])
	if m == nil {
		return Target{}, fmt.Errorf("permalink에 메시지 ts가 없다: %s", s)
	}
	d := m[1]
	t := Target{Kind: TargetPermalink, ID: parts[1], TS: d[:len(d)-6] + "." + d[len(d)-6:]}
	if th := u.Query().Get("thread_ts"); th != "" && th != t.TS {
		if !TSRe.MatchString(th) {
			return Target{}, fmt.Errorf("permalink의 thread_ts가 ts 형식이 아니다: %s", th)
		}
		t.ThreadTS = th
	}
	return t, nil
}
