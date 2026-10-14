package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/spf13/cobra"
)

func (a *app) watchCmd() *cobra.Command {
	var all, notify bool
	cmd := &cobra.Command{
		Use:   "watch [대상…]",
		Short: "새 메시지를 실시간으로 — 기본은 DM·멘션·내 스레드 답글만",
		Long: `새 메시지를 한 줄씩 출력한다(--json이면 NDJSON). user 프로필은 Socket Mode라 앱 토큰(xapp-)이 필요하고,
session 프로필(gslack auth import, 또는 GSLACK_TOKEN=xoxc-…와 GSLACK_COOKIE)은 RTM으로 앱 토큰 없이 붙는다(비공식 — Slack이 막으면 깨진다).

기본: DM, 그룹 DM, <@나>, @here/@channel/@everyone, 내가 속한 그룹 멘션, 내가 시작한 스레드의 답글.
대상을 주면 그 대화의 모든 메시지도, --all이면 가입한 모든 대화의 메시지를 보여 준다.
내 메시지와 수정·삭제 이벤트는 거른다.

한계:
  - 끊긴 동안 온 메시지는 다시 오지 않는다(재연결은 자동) — 빈틈은 gslack unread로.
  - 실행 중 새로 가입한 채널, 내가 답글만 단 스레드는 잡지 않는다.
  - RTM은 실행 중 새로 생긴 그룹 DM을 멘션 없이는 보여 주지 않고, 가입하지 않은 채널의 멘션은 오지 않는다.
  - Socket Mode 연결은 내 접속 상태(presence)를 active로 만들지 않는다. RTM은 연결 중 active로 보였다.`,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			if notify && runtime.GOOS != "darwin" {
				return usagef("--notify는 macOS에서만")
			}
			targets := make([]core.Target, len(args))
			for i, arg := range args {
				t, err := core.ParseTarget(arg)
				if err != nil {
					return usageError{err}
				}
				targets[i] = t
			}
			ctx := cmd.Context()
			s, err := a.open(ctx)
			if err != nil {
				return err
			}
			dialer, err := liveDialer(s) // 네트워크 전에 — 빨리 실패
			if err != nil {
				return err
			}
			w, err := a.newWatcher(ctx, s)
			if err != nil {
				return err
			}
			w.all = all
			mode := "DM·멘션·내 스레드 답글"
			for i, t := range targets {
				id, err := s.channelID(ctx, t)
				if err != nil {
					return err
				}
				if !w.member[id] {
					return usagef("%s: 가입한 대화만 볼 수 있다", args[i])
				}
				w.targets[id] = true
				mode += " + " + args[i]
			}
			if all {
				mode += " + 모든 대화"
			}
			fmt.Fprintf(a.errw, "watching @%s — %s (Ctrl-C로 끝)\n", s.prof.User, mode)
			return a.listen(ctx, s, dialer, func(e *slackevents.MessageEvent) error { return a.emit(ctx, s, w, e, notify) },
				func() {
					fmt.Fprintln(a.errw, "gslack: 다시 연결됨 — 끊긴 동안의 메시지는 gslack unread로")
				})
		}),
	}
	cmd.Flags().BoolVar(&all, "all", false, "가입한 모든 대화의 모든 메시지")
	cmd.Flags().BoolVar(&notify, "notify", false, "DM·멘션이면 macOS 알림")
	return cmd
}

// liveDialer는 실시간 소스를 고른다: session이면 RTM(앱 설치가 필요 없다), 아니면 Socket Mode(nil). 네트워크는 부르지 않는다.
func liveDialer(s *session) (*websocket.Dialer, error) {
	if s.prof.Kind == "session" {
		return rtmDialer(s.cookie)
	}
	if s.app == "" {
		return nil, usagef("실시간에는 앱 토큰(xapp-)이 필요하다 — gslack auth login에서 입력하거나 GSLACK_APP_TOKEN(또는 session 프로필: gslack auth import)")
	}
	return nil, nil
}

// newWatcher는 가입 대화(새로 받는다 — 오래 도는 명령이라 캐시를 믿지 않는다)·내 그룹·사용자 목록을 채운다.
// 그룹·사용자 실패는 경고만 — 그룹을 모르면 모든 그룹 멘션을, 이름은 users.info로.
func (a *app) newWatcher(ctx context.Context, s *session) (*watcher, error) {
	if err := s.loadChannels(ctx, true); err != nil {
		return nil, err
	}
	w := &watcher{me: s.prof.UserID, member: map[string]bool{}, targets: map[string]bool{}}
	for _, c := range s.chans {
		w.member[c.ID] = true
	}
	var err error
	if w.groups, err = s.myGroups(ctx); err != nil { // 오류면 nil — 모든 그룹 멘션 통과
		if se := (slack.SlackErrorResponse{}); errors.As(err, &se) && se.Err == "missing_scope" {
			fmt.Fprintln(a.errw, "gslack: usergroups:read가 없어 모든 그룹 멘션을 보여 준다(앱 재설치 필요)")
		} else {
			fmt.Fprintf(a.errw, "gslack: 그룹 목록 실패(%v) — 모든 그룹 멘션을 보여 준다\n", err)
		}
	}
	if err := s.loadUsers(ctx, false); err != nil { // 첫 이벤트 때 users.list 전체를 받지 않게
		fmt.Fprintf(a.errw, "gslack: 사용자 목록 실패(%v)\n", err)
		s.users = map[string]core.User{} // 비워 두면 이름마다 users.list를 다시 부른다 — users.info 폴백으로
	}
	return w, nil
}

// listen은 dialer가 있으면 RTM, 없으면 Socket Mode로 붙어 메시지마다 on을, 다시 붙을 때마다 reconnected를 부른다.
func (a *app) listen(ctx context.Context, s *session, dialer *websocket.Dialer, on func(*slackevents.MessageEvent) error, reconnected func()) error {
	if dialer != nil {
		return a.watchRTM(ctx, s, dialer, on, reconnected)
	}
	return a.watchSocket(ctx, s, on, reconnected)
}

func (a *app) watchSocket(ctx context.Context, s *session, on func(*slackevents.MessageEvent) error, reconnected func()) error {
	sm := socketmode.New(s.api)
	errc := make(chan error, 1)
	go func() { errc <- sm.RunContext(ctx) }()

	seen := map[string]bool{} // event_id — 재연결 때 재전송 대비
	for {
		var evt socketmode.Event
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		case evt = <-sm.Events:
		}
		switch d := evt.Data.(type) {
		case *slack.ConnectionErrorEvent:
			if errors.Is(d.ErrorObj, context.Canceled) {
				continue
			}
			if err := refused(d.ErrorObj); err != nil {
				return err
			}
			fmt.Fprintf(a.errw, "gslack: 연결 실패 — %s 뒤 다시 시도(%v)\n", d.Backoff, d.ErrorObj)
		case *socketmode.ConnectedEvent:
			if d.ConnectionCount > 0 {
				reconnected()
			}
		case slackevents.EventsAPIEvent:
			// 3초 안에 — 이름 조회(users.info)보다 먼저. ctx를 넘겨야 끊긴 연결에서 막혀도 Ctrl-C가 듣는다
			if err := sm.AckCtx(ctx, evt.Request.EnvelopeID, nil); err != nil {
				continue
			}
			if cb, ok := d.Data.(*slackevents.EventsAPICallbackEvent); ok {
				if seen[cb.EventID] {
					continue
				}
				if len(seen) >= 1000 {
					clear(seen)
				}
				seen[cb.EventID] = true
			}
			if e, ok := d.InnerEvent.Data.(*slackevents.MessageEvent); ok {
				if err := on(e); err != nil {
					return err
				}
			}
		}
	}
}

// watchRTM은 session(xoxc+d)으로 RTM에 붙는다. 이벤트 채널은 닫히지 않으므로 ctx와 인증 실패로만 끝난다.
func (a *app) watchRTM(ctx context.Context, s *session, dialer *websocket.Dialer, on func(*slackevents.MessageEvent) error, reconnected func()) error {
	r := s.api.NewRTM(slack.RTMOptionDialer(dialer))
	go r.ManageConnection()
	kind := make(map[string]string, len(s.chans)) // RTM 메시지에는 channel_type이 없다
	for _, c := range s.chans {
		kind[c.ID] = c.Kind
	}
	for {
		var evt slack.RTMEvent
		select {
		case <-ctx.Done():
			// ponytail: 재연결만 멈춘다. rtm.connect·dial은 context가 없어(최대 10초) 기다리지 않는다 — 프로세스가 끝난다
			go r.Disconnect()
			return nil
		case evt = <-r.IncomingEvents:
		}
		switch d := evt.Data.(type) {
		case *slack.ConnectionErrorEvent:
			if err := refused(d.ErrorObj); err != nil {
				return err
			}
			fmt.Fprintf(a.errw, "gslack: 연결 실패 — %s 뒤 다시 시도(%v)\n", d.Backoff, d.ErrorObj)
		case *slack.ConnectedEvent:
			if d.ConnectionCount > 0 {
				reconnected()
			}
		case *slack.InvalidAuthEvent: // 이 뒤 slack-go는 조용히 멈춘다
			return slack.SlackErrorResponse{Err: "invalid_auth"}
		case *slack.MessageEvent:
			typ := kind[d.Channel]
			if typ == "" && strings.HasPrefix(d.Channel, "D") { // 실행 중 새로 열린 DM
				typ = core.KindIM
			}
			e := &slackevents.MessageEvent{Channel: d.Channel, ChannelType: typ, User: d.User, Text: d.Text,
				SubType: d.SubType, Message: &d.Msg, Root: d.Root}
			if err := on(e); err != nil {
				return err
			}
		}
	}
}

// refused는 Slack이 연결을 ok:false로 거절한 오류다(RTM 차단·토큰 종류 등). slack-go는 인증 오류 몇 개 말고는
// 영원히 재시도하므로 여기서 끝낸다. 네트워크·429·5xx와 Slack이 일시적이라 밝힌 코드는 재시도에 맡긴다.
func refused(err error) error {
	var se slack.SlackErrorResponse
	if errors.As(err, &se) && !slices.Contains([]string{"internal_error", "fatal_error", "request_timeout", "service_unavailable"}, se.Err) {
		return se
	}
	return nil
}

// emit은 걸러서 한 줄(--json이면 NDJSON)을 내고, 나에게 온 것이면 알린다.
func (a *app) emit(ctx context.Context, s *session, w *watcher, e *slackevents.MessageEvent, notify bool) error {
	show, alert := w.keep(e)
	if !show {
		return nil
	}
	raw := slack.Message{Msg: *e.Message}
	m := s.message(ctx, raw, e.Channel)
	where, body := s.where(e, m), preview(withTags(s.render(ctx, m.Text), m), 120)
	if m.ThreadTS != "" && m.ThreadTS != m.TS {
		body = "↳ " + body
	}
	if a.asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(m); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(a.out, "%-17s  %s  %s  @%s: %s\n", m.TS, core.Time(m.TS).Local().Format("01/02 15:04"), where, m.UserName, body)
	}
	if notify && alert {
		if err := notifyFn(where+" @"+m.UserName, body); err != nil {
			fmt.Fprintln(a.errw, "gslack: 알림 실패:", err)
		}
	}
	return nil
}

// rtmDialer는 RTM 웹소켓에 d 쿠키를 싣는다. 웹소켓 호스트(wss-primary.slack.com 등)가 API와 달라
// Domain .slack.com으로 넣는다(host-only면 안 간다). 쿠키가 없으면 조용한 재연결 루프 대신 바로 오류.
func rtmDialer(cookie string) (*websocket.Dialer, error) {
	var cs []*http.Cookie
	for _, kv := range strings.Split(cookie, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			cs = append(cs, &http.Cookie{Name: k, Value: v, Domain: ".slack.com", Path: "/"})
		}
	}
	if len(cs) == 0 {
		return nil, usagef("session 프로필에 쿠키가 없다 — gslack auth import(또는 GSLACK_COOKIE)")
	}
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(&url.URL{Scheme: "https", Host: "slack.com"}, cs)
	d := *websocket.DefaultDialer
	d.Jar = jar
	return &d, nil
}

// notifyFn은 macOS 알림이다. 본문은 argv로 넘겨 AppleScript 소스에 끼우지 않는다(따옴표 주입 없음).
var notifyFn = func(title, body string) error {
	return exec.Command("osascript", "-e", "on run argv", "-e",
		"display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body).Run()
}

// where는 출력용 대화 이름이다. 내 메시지는 걸렀으니 DM의 보낸 사람이 곧 상대다.
func (s *session) where(e *slackevents.MessageEvent, m core.Message) string {
	if e.ChannelType == "im" {
		return "@" + m.UserName
	}
	return "#" + cmp.Or(s.channelName(e.Channel), e.Channel)
}

// myGroups는 내가 속한 사용자 그룹 ID다.
func (s *session) myGroups(ctx context.Context) (map[string]bool, error) {
	gs, err := s.loadGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, g := range gs {
		for _, u := range g.Users {
			if u == s.prof.UserID {
				out[g.ID] = true
			}
		}
	}
	return out, nil
}

var mentionRe = regexp.MustCompile(`<(@[UW][A-Z0-9]+|!here|!channel|!everyone|!subteam\^S[A-Z0-9]+)(?:\|[^>]*)?>`)

type watcher struct {
	me              string
	all             bool
	member, targets map[string]bool // 가입 대화, 명령 인자 대화
	groups          map[string]bool // 내 그룹. nil이면 모름 — 모든 그룹 멘션을 통과
}

// keep은 보여 줄지(show)와 나에게 온 것(DM·멘션·내 스레드 답글)인지(alert)를 정한다.
func (w *watcher) keep(e *slackevents.MessageEvent) (show, alert bool) {
	if e.Message == nil {
		return false, false
	}
	switch e.SubType {
	case "", "thread_broadcast", "bot_message", "file_share", "me_message":
	default: // message_changed·deleted, channel_join 등
		return false, false
	}
	if e.User == w.me {
		return false, false
	}
	if e.ChannelType == "im" || e.ChannelType == "mpim" || e.Message.ParentUserId == w.me ||
		e.Root != nil && e.Root.User == w.me { // thread_broadcast에는 parent_user_id 대신 root가 온다
		return true, true
	}
	text := cmp.Or(e.Text, fallbackText(slack.Message{Msg: *e.Message}))
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		tok := m[1]
		switch {
		case tok == "@"+w.me: // 가입하지 않은 채널에서 불러도 알린다(Slack 앱과 같다)
			return true, true
		case !w.member[e.Channel]: // 브로드캐스트·그룹 멘션은 가입 대화에서만
		case strings.HasPrefix(tok, "!subteam^"):
			if w.groups == nil || w.groups[strings.TrimPrefix(tok, "!subteam^")] {
				return true, true
			}
		case strings.HasPrefix(tok, "!"):
			return true, true
		}
	}
	return w.member[e.Channel] && (w.all || w.targets[e.Channel]), false
}
