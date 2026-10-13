package core

import (
	"encoding/json"
	"testing"
)

func TestParseTarget(t *testing.T) {
	ok := []struct {
		in   string
		want Target
	}{
		{"#general", Target{Kind: TargetChannelName, Name: "general"}},
		{"#일반", Target{Kind: TargetChannelName, Name: "일반"}},
		{"general", Target{Kind: TargetChannelName, Name: "general"}},
		{"@jaekwon", Target{Kind: TargetUserName, Name: "jaekwon"}},
		{"@U0ABCDEFGHI", Target{Kind: TargetUserID, ID: "U0ABCDEFGHI"}},
		{"C0ABCDEFG", Target{Kind: TargetChannelID, ID: "C0ABCDEFG"}},
		{"D0C61KGQ72NABC", Target{Kind: TargetChannelID, ID: "D0C61KGQ72NABC"}}, // 길이 상한 없음
		{"W012A3CDE", Target{Kind: TargetUserID, ID: "W012A3CDE"}},
		{"https://acme.slack.com/archives/C0ABCDEFG/p1790823272576877",
			Target{Kind: TargetPermalink, ID: "C0ABCDEFG", TS: "1790823272.576877"}},
		{"https://acme.enterprise.slack.com/archives/C0ABCDEFG/p1790823272576877?thread_ts=1790823272.576877&cid=C0ABCDEFG",
			Target{Kind: TargetPermalink, ID: "C0ABCDEFG", TS: "1790823272.576877"}}, // 부모 자신
		{"https://acme.slack.com/archives/C0ABCDEFG/p1790823299000001?thread_ts=1790823272.576877&cid=C0ABCDEFG",
			Target{Kind: TargetPermalink, ID: "C0ABCDEFG", TS: "1790823299.000001", ThreadTS: "1790823272.576877"}},
	}
	for _, c := range ok {
		got, err := ParseTarget(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseTarget(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "#", "@",
		"https://evilslack.com/archives/C0ABCDEFG/p1790823272576877",
		"https://slack.com.evil.com/archives/C0ABCDEFG/p1790823272576877",
		"https://acme.slack.com/archives/C0ABCDEFG",
		"https://acme.slack.com/archives/C0ABCDEFG/x123",
		"https://acme.slack.com/team/U0ABCDEFGHI",
		"https://acme.slack.com/archives/C0ABCDEFG/p1234567",
		"https://acme.slack.com/archives/C0ABCDEFG/p1790823272576877?thread_ts=x&cid=C0ABCDEFG",
	} {
		if got, err := ParseTarget(in); err == nil {
			t.Errorf("ParseTarget(%q) = %+v; want error", in, got)
		}
	}
}

func TestRender(t *testing.T) {
	user := func(id string) string { return map[string]string{"U1AAAAAAAA": "jaekwon"}[id] }
	ch := func(id string) string { return map[string]string{"C1AAAAAAAA": "general"}[id] }
	group := func(id string) string { return map[string]string{"S1AAAAAAAA": "devs"}[id] }
	cases := []struct{ in, want string }{
		{"hi <@U1AAAAAAAA>", "hi @jaekwon"},
		{"hi <@W9ZZZZZZZZ>", "hi @W9ZZZZZZZZ"},
		{"<@U1AAAAAAAA|jk>", "@jk"},
		{"see <#C1AAAAAAAA>, <#C2BBBBBBBB|random>", "see #general, #random"},
		{"<!here> <!channel> <!everyone>", "@here @channel @everyone"},
		{"<!subteam^S123|@devs> ping", "@devs ping"},
		{"<!subteam^S1AAAAAAAA> <!subteam^S2BBBBBBBB>", "@devs @subteam"},
		{"<!date^1392734382^{date}|Feb 18, 2014>", "Feb 18, 2014"},
		{"<mailto:a@b.com|a@b.com>", "a@b.com"},
		{"<https://x.com/?a=1&amp;b=2|docs>", "docs (https://x.com/?a=1&b=2)"},
		{"<https://x.com>", "https://x.com"},
		{"a &lt;b&gt; &amp;amp;", "a <b> &amp;"},
	}
	for _, c := range cases {
		if got := Render(c.in, user, ch, group); got != c.want {
			t.Errorf("Render(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestMessageJSON(t *testing.T) {
	b, _ := json.Marshal(Message{Channel: "C1", TS: "1.2"})
	if got, want := string(b), `{"channel":"C1","ts":"1.2","text":""}`; got != want {
		t.Errorf("json = %s; want %s", got, want)
	}
}

func TestPermalinkAndTime(t *testing.T) {
	if got := Permalink("https://acme.slack.com/", "C1", "1790823272.576877", ""); got != "https://acme.slack.com/archives/C1/p1790823272576877" {
		t.Error(got)
	}
	if got := Permalink("https://acme.slack.com/", "C1", "2.000001", "1.000000"); got != "https://acme.slack.com/archives/C1/p2000001?thread_ts=1.000000&cid=C1" {
		t.Error(got)
	}
	if got := Time("1790823272.576877"); got.Unix() != 1790823272 || got.Nanosecond() != 576877000 {
		t.Error(got)
	}
}
