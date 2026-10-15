package cli

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
)

// slack-go 타입 → core 정규화 모델

func userFrom(u slack.User) core.User {
	return core.User{
		ID: u.ID, Name: u.Name, RealName: u.RealName, DisplayName: u.Profile.DisplayName,
		Title: u.Profile.Title, StatusText: u.Profile.StatusText, StatusEmoji: u.Profile.StatusEmoji,
		TZ: u.TZ, Bot: u.IsBot, Deleted: u.Deleted,
	}
}

func channelFrom(c slack.Channel) core.Channel {
	kind := core.KindPublic
	switch {
	case c.IsIM:
		kind = core.KindIM
	case c.IsMpIM:
		kind = core.KindMPIM
	case c.IsPrivate || c.IsGroup:
		kind = core.KindPrivate
	}
	return core.Channel{ID: c.ID, Name: c.Name, Kind: kind, User: c.User, Shared: c.IsExtShared || c.IsShared}
}

// message는 history 응답의 메시지를 정규화한다. history 메시지에는 channel이 없어서 주입한다.
func (s *session) message(ctx context.Context, m slack.Message, channel string) core.Message {
	out := core.Message{
		Channel: channel, TS: m.Timestamp, ThreadTS: m.ThreadTimestamp, User: m.User,
		Text: m.Text, Subtype: m.SubType, ReplyCount: m.ReplyCount, Edited: m.Edited != nil, Team: m.Team,
		Permalink: core.Permalink(s.prof.URL, channel, m.Timestamp, m.ThreadTimestamp),
	}
	if out.ThreadTS == out.TS && out.ReplyCount == 0 {
		out.ThreadTS = ""
	}
	switch {
	case m.User != "":
		out.UserName = s.userName(ctx, m.User)
	case m.BotID != "":
		out.User, out.Bot = m.BotID, true
		out.UserName = m.Username
		if m.BotProfile != nil && m.BotProfile.Name != "" {
			out.UserName = m.BotProfile.Name
		}
		if out.UserName == "" { // cmp.Or는 인자를 먼저 다 평가한다 — 이름을 아는 봇에 bots.info를 부르지 않게
			out.UserName = cmp.Or(s.botName(ctx, m.BotID), m.BotID)
		}
	}
	if out.Text == "" {
		out.Text = fallbackText(m)
	}
	for _, f := range m.Files {
		out.Files = append(out.Files, core.File{ID: f.ID, Name: f.Name, Mimetype: f.Mimetype})
	}
	for _, r := range m.Reactions {
		out.Reactions = append(out.Reactions, core.Reaction{Name: r.Name, Count: r.Count})
	}
	return out
}

// fallbackText는 text가 빈 메시지(blocks·attachments만 있는 봇 메시지)에서 평문을 뽑는다.
func fallbackText(m slack.Message) string {
	var parts []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	for _, b := range m.Blocks.BlockSet {
		switch b := b.(type) {
		case *slack.HeaderBlock:
			if b.Text != nil {
				add(b.Text.Text)
			}
		case *slack.SectionBlock:
			if b.Text != nil {
				add(b.Text.Text)
			}
			for _, f := range b.Fields {
				add(f.Text)
			}
		case *slack.RichTextBlock: // text가 빈 메시지도 있다(예: Slackbot 허들 알림 — 2026-10-12 실측)
			add(richText(b.Elements))
		}
	}
	if len(parts) == 0 {
		for _, a := range m.Attachments {
			add(strings.Join([]string{a.Pretext, a.Title, a.Text}, "\n"))
			if a.Pretext == "" && a.Title == "" && a.Text == "" {
				add(a.Fallback)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// richText는 rich_text 블록의 글자를 mrkdwn으로 모은다(목록·인용·코드는 줄 단위로만).
func richText(els []slack.RichTextElement) string {
	var b strings.Builder
	for _, el := range els {
		var items []slack.RichTextSectionElement
		switch el := el.(type) {
		case *slack.RichTextSection:
			items = el.Elements
		case *slack.RichTextQuote:
			items = el.Elements
		case *slack.RichTextPreformatted:
			items = el.Elements
		case *slack.RichTextList:
			for _, item := range el.Elements {
				b.WriteString("• " + richText([]slack.RichTextElement{item}))
			}
			continue
		}
		for _, e := range items {
			switch e := e.(type) {
			case *slack.RichTextSectionTextElement:
				b.WriteString(e.Text)
			case *slack.RichTextSectionLinkElement:
				b.WriteString("<" + e.URL + "|" + cmp.Or(e.Text, e.URL) + ">")
			case *slack.RichTextSectionUserElement:
				b.WriteString("<@" + e.UserID + ">")
			case *slack.RichTextSectionChannelElement:
				b.WriteString("<#" + e.ChannelID + ">")
			case *slack.RichTextSectionUserGroupElement:
				b.WriteString("<!subteam^" + e.UsergroupID + ">")
			case *slack.RichTextSectionBroadcastElement:
				b.WriteString("<!" + e.Range + ">")
			case *slack.RichTextSectionEmojiElement:
				b.WriteString(":" + e.Name + ":")
				if e.SkinTone >= 2 { // Slack 원문 꼴 — core.Emoji가 표에서 변형을 찾는다
					fmt.Fprintf(&b, ":skin-tone-%d:", e.SkinTone)
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
