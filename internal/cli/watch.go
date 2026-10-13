package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

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
		Long: `Socket Mode로 새 메시지를 한 줄씩 출력한다(--json이면 NDJSON). 앱 토큰(xapp-)이 필요하다.

기본: DM, 그룹 DM, <@나>, @here/@channel/@everyone, 내가 속한 그룹 멘션, 내가 시작한 스레드의 답글.
대상을 주면 그 대화의 모든 메시지도, --all이면 가입한 모든 대화의 메시지를 보여 준다.
내 메시지와 수정·삭제 이벤트는 거른다.

한계:
  - 끊긴 동안 온 메시지는 다시 오지 않는다(재연결은 자동) — 빈틈은 gslack unread로.
  - 실행 중 새로 가입한 채널, 내가 답글만 단 스레드는 잡지 않는다.
  - Socket Mode 연결은 내 접속 상태(presence)를 active로 만들지 않는다.`,
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
			if s.app == "" {
				return usagef("watch에는 앱 토큰(xapp-)이 필요하다 — gslack auth login에서 입력하거나 GSLACK_APP_TOKEN")
			}
			if err := s.loadChannels(ctx, true); err != nil { // 오래 도는 명령이라 캐시를 믿지 않는다
				return err
			}
			w := &watcher{me: s.prof.UserID, all: all, member: map[string]bool{}, targets: map[string]bool{}}
			for _, c := range s.chans {
				w.member[c.ID] = true
			}
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
			w.groups, err = s.myGroups(ctx)
			if se := (slack.SlackErrorResponse{}); errors.As(err, &se) && se.Err == "missing_scope" {
				fmt.Fprintln(a.errw, "gslack: usergroups:read가 없어 모든 그룹 멘션을 보여 준다(앱 재설치 필요)")
			} else if err != nil {
				return err
			}
			if err := s.loadUsers(ctx, false); err != nil { // 첫 이벤트 때 users.list 전체를 받지 않게
				return err
			}
			fmt.Fprintf(a.errw, "watching @%s — %s (Ctrl-C로 끝)\n", s.prof.User, mode)
			return a.watch(ctx, s, w, notify)
		}),
	}
	cmd.Flags().BoolVar(&all, "all", false, "가입한 모든 대화의 모든 메시지")
	cmd.Flags().BoolVar(&notify, "notify", false, "DM·멘션이면 macOS 알림")
	return cmd
}

func (a *app) watch(ctx context.Context, s *session, w *watcher, notify bool) error {
	sm := socketmode.New(s.api)
	errc := make(chan error, 1)
	go func() { errc <- sm.RunContext(ctx) }()

	enc := json.NewEncoder(a.out)
	enc.SetEscapeHTML(false)
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
			fmt.Fprintf(a.errw, "gslack: 연결 실패 — %s 뒤 다시 시도(%v)\n", d.Backoff, d.ErrorObj)
		case *socketmode.ConnectedEvent:
			if d.ConnectionCount > 0 {
				fmt.Fprintln(a.errw, "gslack: 다시 연결됨 — 끊긴 동안의 메시지는 gslack unread로")
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
			e, ok := d.InnerEvent.Data.(*slackevents.MessageEvent)
			if !ok || e.Message == nil {
				continue
			}
			raw := slack.Message{Msg: *e.Message}
			show, alert := w.keep(e, cmp.Or(e.Text, fallbackText(raw)))
			if !show {
				continue
			}
			m := s.message(ctx, raw, e.Channel)
			text := core.Render(m.Text, func(id string) string { return s.userName(ctx, id) }, s.channelName)
			where, body := s.where(e, m), preview(withTags(text, m), 120)
			if m.ThreadTS != "" && m.ThreadTS != m.TS {
				body = "↳ " + body
			}
			if a.asJSON {
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
		}
	}
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
	gs, err := s.api.GetUserGroupsContext(ctx, slack.GetUserGroupsOptionIncludeUsers(true))
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
func (w *watcher) keep(e *slackevents.MessageEvent, text string) (show, alert bool) {
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
