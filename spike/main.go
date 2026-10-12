// M0 spike(A 경로). 버리는 코드다 — 가정 검증 결과만 남긴다.
//
//	GSLACK_TOKEN=xoxp-… go run . auth            # 0.2 auth.test + users.conversations
//	GSLACK_TOKEN=xoxp-… go run . burst C123      # 0.2 history limit=200 × 16회/분
//	GSLACK_TOKEN=xoxp-… go run . unread          # 0.6 last_read / unread_count 확인
//	GSLACK_TOKEN=xoxp-… GSLACK_APP_TOKEN=xapp-… go run . watch   # 0.4 Socket Mode
package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: spike auth|burst <channel>|unread|watch")
		os.Exit(2)
	}
	api := slack.New(mustEnv("GSLACK_TOKEN"), slack.OptionAppLevelToken(os.Getenv("GSLACK_APP_TOKEN")))
	me, err := api.AuthTest()
	check(err)
	fmt.Printf("auth.test ok: user=%s(%s) team=%s url=%s\n", me.User, me.UserID, me.Team, me.URL)

	switch os.Args[1] {
	case "auth":
		chans := myConversations(api)
		fmt.Printf("users.conversations: %d개\n", len(chans))
		for i, c := range chans[:min(10, len(chans))] {
			fmt.Printf("  %2d %s %-6s %s\n", i, c.ID, kind(c), c.Name)
		}
	case "burst":
		burst(api, os.Args[2])
	case "unread":
		unread(api)
	case "watch":
		watch(api, me.UserID)
	}
}

func myConversations(api *slack.Client) []slack.Channel {
	var all []slack.Channel
	p := &slack.GetConversationsForUserParameters{Types: []string{"public_channel", "private_channel", "im", "mpim"}, Limit: 200, ExcludeArchived: true}
	for {
		chans, next, err := api.GetConversationsForUser(p)
		check(err)
		all = append(all, chans...)
		if next == "" {
			return all
		}
		p.Cursor = next
	}
}

// 0.2: 429 헤더는 429일 때만 오므로, 실제로 몰아서 호출해 본다.
func burst(api *slack.Client, channel string) {
	start := time.Now()
	for i := 1; i <= 16; i++ {
		res, err := api.GetConversationHistory(&slack.GetConversationHistoryParameters{ChannelID: channel, Limit: 200})
		var rl *slack.RateLimitedError
		if errors.As(err, &rl) {
			fmt.Printf("FAIL #%d: 429 retry-after=%s (%.1fs 경과)\n", i, rl.RetryAfter, time.Since(start).Seconds())
			return
		}
		check(err)
		fmt.Printf("#%2d %3d건 has_more=%v %.1fs\n", i, len(res.Messages), res.HasMore, time.Since(start).Seconds())
	}
	fmt.Printf("PASS: 16회/%.1fs, 429 없음\n", time.Since(start).Seconds())
}

// 0.6: conversations.info가 채널에 last_read를 주는지, DM에 unread_count를 주는지.
func unread(api *slack.Client) {
	seen := map[string]int{}
	for _, c := range myConversations(api) {
		k := kind(c)
		if seen[k] >= 3 {
			continue
		}
		seen[k]++
		info, err := api.GetConversationInfo(&slack.GetConversationInfoInput{ChannelID: c.ID})
		check(err)
		fmt.Printf("%-6s %s last_read=%q unread_count=%d unread_display=%d\n", k, c.ID, info.LastRead, info.UnreadCount, info.UnreadCountDisplay)
	}
}

// 0.4: user 이벤트 수신, ack, 재접속, 자기 메시지 필터, 프레즌스.
func watch(api *slack.Client, myID string) {
	sm := socketmode.New(api)
	go func() {
		for evt := range sm.Events {
			switch evt.Type {
			case socketmode.EventTypeConnecting, socketmode.EventTypeConnected, socketmode.EventTypeDisconnect:
				fmt.Println("[conn]", evt.Type)
				if evt.Type == socketmode.EventTypeConnected {
					go presence(api, myID)
				}
			case socketmode.EventTypeEventsAPI:
				sm.Ack(*evt.Request) // 3초 안에 ack
				ev, _ := evt.Data.(slackevents.EventsAPIEvent)
				id := ""
				if cb, ok := ev.Data.(*slackevents.EventsAPICallbackEvent); ok {
					id = cb.EventID
				}
				msg, ok := ev.InnerEvent.Data.(*slackevents.MessageEvent)
				if !ok {
					fmt.Printf("[evt %s] %s\n", id, ev.InnerEvent.Type)
					continue
				}
				if msg.User == myID {
					fmt.Printf("[self, filtered %s]\n", id)
					continue
				}
				fmt.Printf("[%s %s %s] %s ts=%s sub=%q: %s\n", id, msg.ChannelType, msg.Channel, msg.User, msg.TimeStamp, msg.SubType, msg.Text)
			}
		}
	}()
	check(sm.Run())
}

func presence(api *slack.Client, myID string) {
	time.Sleep(5 * time.Second)
	p, err := api.GetUserPresence(myID)
	if err != nil {
		fmt.Println("[presence] err:", err)
		return
	}
	fmt.Printf("[presence] %s online=%v auto_away=%v connections=%d\n", p.Presence, p.Online, p.AutoAway, p.ConnectionCount)
}

func kind(c slack.Channel) string {
	switch {
	case c.IsIM:
		return "im"
	case c.IsMpIM:
		return "mpim"
	case c.IsPrivate:
		return "group"
	}
	return "public"
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		fmt.Fprintf(os.Stderr, "%s 환경 변수가 필요하다\n", k)
		os.Exit(2)
	}
	return v
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
