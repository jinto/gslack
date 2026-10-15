// M0 spike(A 경로). 버리는 코드다 — 가정 검증 결과만 남긴다.
//
//	GSLACK_TOKEN=xoxp-… go run . auth            # 0.2 auth.test + users.conversations
//	GSLACK_TOKEN=xoxp-… go run . burst C123      # 0.2 history limit=200 × 16회/분
//	GSLACK_TOKEN=xoxp-… go run . unread          # 0.6 last_read / unread_count 확인
//	GSLACK_TOKEN=xoxp-… GSLACK_APP_TOKEN=xapp-… go run . watch   # 0.4 Socket Mode
//
// B 경로: GSLACK_TOKEN=xoxc-… GSLACK_COOKIE=xoxd-… 를 주면 모든 요청에 d 쿠키를 붙인다.
//
//	go run . auth      # 0.3 조회
//	go run . counts    # 0.8 client.counts(비공식)
//	go run . rtm       # 0.5 RTM(쿠키 Jar 다이얼러) — 메시지 본문은 찍지 않는다
//	go run . mute      # M4 T4: users.prefs.get의 뮤트 채널, client.counts has_unreads와 겹치는지(xoxp도 시도)
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: spike auth|burst <channel>|unread|watch|counts|rtm|mute|later [permalink | 채널ID ts]")
		os.Exit(2)
	}
	api := slack.New(mustEnv("GSLACK_TOKEN"), slack.OptionAppLevelToken(os.Getenv("GSLACK_APP_TOKEN")),
		slack.OptionHTTPClient(&http.Client{Transport: cookieRT{}, Timeout: 30 * time.Second}))
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
	case "counts":
		counts(me.URL)
	case "rtm":
		rtm(api, me.UserID)
	case "mute":
		mute(me.URL)
	case "later":
		later(me.URL, os.Args[2:])
	}
}

// d 쿠키(xoxd 값 또는 raw Cookie 헤더). 없으면 아무것도 안 붙인다(xoxp).
func cookie() string {
	c := strings.TrimSpace(os.Getenv("GSLACK_COOKIE"))
	if strings.HasPrefix(c, "xoxd-") {
		return "d=" + c
	}
	return c
}

type cookieRT struct{}

func (cookieRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if c := cookie(); c != "" && strings.HasSuffix(r.URL.Hostname(), "slack.com") {
		r = r.Clone(r.Context())
		r.Header.Set("Cookie", c)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// 0.8: client.counts는 slack-go에 없다 — 직접 POST. 개수만 찍는다.
func counts(teamURL string) {
	form := url.Values{"token": {os.Getenv("GSLACK_TOKEN")}}
	req, _ := http.NewRequest("POST", strings.TrimRight(teamURL, "/")+"/api/client.counts", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start := time.Now()
	res, err := (&http.Client{Transport: cookieRT{}, Timeout: 30 * time.Second}).Do(req)
	check(err)
	defer res.Body.Close()
	var body struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Channels []struct {
			ID           string `json:"id"`
			HasUnreads   bool   `json:"has_unreads"`
			MentionCount int    `json:"mention_count"`
		} `json:"channels"`
		MPIMs []struct {
			ID           string `json:"id"`
			HasUnreads   bool   `json:"has_unreads"`
			MentionCount int    `json:"mention_count"`
		} `json:"mpims"`
		IMs []struct {
			ID           string `json:"id"`
			HasUnreads   bool   `json:"has_unreads"`
			MentionCount int    `json:"mention_count"`
		} `json:"ims"`
		Threads struct {
			HasUnreads   bool `json:"has_unreads"`
			MentionCount int  `json:"mention_count"`
		} `json:"threads"`
	}
	check(json.NewDecoder(res.Body).Decode(&body))
	fmt.Printf("client.counts ok=%v error=%q %.1fs\n", body.OK, body.Error, time.Since(start).Seconds())
	tally := func(name string, n int, unread, mention int) {
		fmt.Printf("  %-8s %4d개  안읽음 %3d  멘션 %3d\n", name, n, unread, mention)
	}
	u, m := 0, 0
	for _, c := range body.Channels {
		if c.HasUnreads {
			u++
		}
		m += c.MentionCount
	}
	tally("channels", len(body.Channels), u, m)
	u, m = 0, 0
	for _, c := range body.MPIMs {
		if c.HasUnreads {
			u++
		}
		m += c.MentionCount
	}
	tally("mpims", len(body.MPIMs), u, m)
	u, m = 0, 0
	for _, c := range body.IMs {
		if c.HasUnreads {
			u++
		}
		m += c.MentionCount
	}
	tally("ims", len(body.IMs), u, m)
	fmt.Printf("  threads  안읽음=%v 멘션 %d\n", body.Threads.HasUnreads, body.Threads.MentionCount)
}

// 0.5: RTM 웹소켓에도 d 쿠키가 필요하다 — Jar(Domain .slack.com)를 다이얼러에.
func rtm(api *slack.Client, myID string) {
	jar, _ := cookiejar.New(nil)
	if c := cookie(); c != "" {
		var cs []*http.Cookie
		for _, kv := range strings.Split(c, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
				cs = append(cs, &http.Cookie{Name: k, Value: v, Domain: ".slack.com", Path: "/"})
			}
		}
		jar.SetCookies(&url.URL{Scheme: "https", Host: "slack.com"}, cs)
	}
	d := *websocket.DefaultDialer
	d.Jar = jar
	r := api.NewRTM(slack.RTMOptionDialer(&d))
	go r.ManageConnection()
	for ev := range r.IncomingEvents {
		switch e := ev.Data.(type) {
		case *slack.ConnectedEvent:
			fmt.Printf("[rtm] connected #%d\n", e.ConnectionCount)
			go presence(api, myID)
		case *slack.ConnectionErrorEvent:
			fmt.Printf("[rtm] connection error: %v (attempt %d)\n", e.ErrorObj, e.Attempt)
		case *slack.InvalidAuthEvent:
			fmt.Println("[rtm] invalid auth")
			os.Exit(1)
		case *slack.MessageEvent:
			who := e.User
			if who == myID {
				who = "나"
			}
			fmt.Printf("[msg] ch=%s user=%s sub=%q ts=%s 길이=%d\n", e.Channel, who, e.SubType, e.Timestamp, len(e.Text))
		case *slack.HelloEvent, *slack.LatencyReport:
		default:
			fmt.Printf("[evt] %s\n", ev.Type)
		}
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

// post는 팀 주소로 token 폼 POST를 보내 JSON을 v에 푼다.
func post(teamURL, method string, v any, kv ...string) {
	form := url.Values{"token": {os.Getenv("GSLACK_TOKEN")}}
	for i := 0; i+1 < len(kv); i += 2 {
		form.Set(kv[i], kv[i+1])
	}
	req, _ := http.NewRequest("POST", strings.TrimRight(teamURL, "/")+"/api/"+method, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := (&http.Client{Transport: cookieRT{}, Timeout: 30 * time.Second}).Do(req)
	check(err)
	defer res.Body.Close()
	check(json.NewDecoder(res.Body).Decode(v))
}

// mute: 뮤트 채널을 어디서 알 수 있는지. 채널 ID와 개수만 찍는다.
func mute(teamURL string) {
	var p struct {
		OK    bool
		Error string
		Prefs map[string]any
	}
	post(teamURL, "users.prefs.get", &p)
	fmt.Printf("users.prefs.get ok=%v error=%q prefs %d개\n", p.OK, p.Error, len(p.Prefs))
	muted := map[string]string{}
	if s, _ := p.Prefs["muted_channels"].(string); s != "" {
		for _, id := range strings.Split(s, ",") {
			muted[id] = "muted_channels"
		}
	}
	fmt.Printf("  muted_channels 키 있음=%v\n", p.Prefs["muted_channels"] != nil)
	if s, _ := p.Prefs["all_notifications_prefs"].(string); s != "" {
		var all struct {
			Channels map[string]struct {
				Muted bool `json:"muted"`
			} `json:"channels"`
		}
		if err := json.Unmarshal([]byte(s), &all); err != nil {
			fmt.Println("  all_notifications_prefs 해석 실패:", err)
		}
		for id, c := range all.Channels {
			if c.Muted {
				muted[id] += "+all_notifications_prefs"
			}
		}
	}
	src := map[string]int{}
	for _, v := range muted {
		src[v]++
	}
	fmt.Printf("  뮤트 채널 %d개 — 출처별 %v\n", len(muted), src)

	var c struct {
		OK       bool
		Error    string
		Channels []struct {
			ID           string `json:"id"`
			HasUnreads   bool   `json:"has_unreads"`
			MentionCount int    `json:"mention_count"`
		}
	}
	post(teamURL, "client.counts", &c)
	fmt.Printf("client.counts ok=%v error=%q channels %d개\n", c.OK, c.Error, len(c.Channels))
	for _, ch := range c.Channels {
		if _, ok := muted[ch.ID]; ok {
			fmt.Printf("  뮤트인데 응답에 있음: %s has_unreads=%v mention=%d\n", ch.ID, ch.HasUnreads, ch.MentionCount)
		}
	}
}

// later: Slack "나중에(Later)"를 어떤 API로 넣고 보는지. 개수·종류·ID만 찍는다(본문 없음).
// 인자 없으면 목록만, permalink(또는 채널ID ts)를 주면 saved.add(안 되면 stars.add)로 넣고 다시 본다.
func later(teamURL string, args []string) {
	list := func() {
		var r struct {
			OK         bool
			Error      string
			SavedItems []struct {
				ItemType string `json:"item_type"`
				ItemID   string `json:"item_id"`
				TS       string
				State    string
			} `json:"saved_items"`
		}
		post(teamURL, "saved.list", &r)
		fmt.Printf("saved.list ok=%v error=%q %d개\n", r.OK, r.Error, len(r.SavedItems))
		for _, it := range r.SavedItems[:min(5, len(r.SavedItems))] {
			fmt.Printf("  %s %s %s %s\n", it.ItemType, it.ItemID, it.TS, it.State)
		}
		var st struct {
			OK    bool
			Error string
			Items []struct{ Type string }
		}
		post(teamURL, "stars.list", &st)
		fmt.Printf("stars.list ok=%v error=%q %d개\n", st.OK, st.Error, len(st.Items))
	}
	list()
	if len(args) == 1 { // permalink …/archives/C123/p1700000000123456
		m := regexp.MustCompile(`/archives/(\w+)/p(\d{10})(\d{6})`).FindStringSubmatch(args[0])
		if m == nil {
			fmt.Println("permalink가 아니다")
			return
		}
		args = []string{m[1], m[2] + "." + m[3]}
	}
	if len(args) < 2 {
		return
	}
	var r struct {
		OK    bool
		Error string
	}
	post(teamURL, "saved.add", &r, "item_type", "message", "item_id", args[0], "ts", args[1])
	fmt.Printf("saved.add ok=%v error=%q\n", r.OK, r.Error)
	if !r.OK {
		post(teamURL, "stars.add", &r, "channel", args[0], "timestamp", args[1])
		fmt.Printf("stars.add ok=%v error=%q\n", r.OK, r.Error)
	}
	list()
}
