package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
	"github.com/spf13/cobra"
)

func (a *app) unreadCmd() *cobra.Command {
	var (
		types []string
		all   bool
	)
	cmd := &cobra.Command{
		Use:   "unread",
		Short: "안 읽은 대화와 최신 메시지(근사치)",
		Long: `기본은 DM(im, mpim)만 본다. --all이나 --type으로 채널까지 넓힌다.
대화마다 conversations.info(+ history)를 불러서 느리다 — 대화가 수백 개면 DM만 수십 초, 전체는 몇 분(Slack 호출 한도).
본인 메시지와 입장/퇴장은 세지 않는다. 스레드 답글과 뮤트 채널은 구분하지 않는다.
한 번도 읽지 않은 채널은 대개 100+로 나온다.`,
		Args: cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, _ []string) error {
			if all && len(types) > 0 {
				return usagef("--all과 --type은 함께 쓸 수 없다")
			}
			if !all && len(types) == 0 {
				types = []string{core.KindIM, core.KindMPIM}
			}
			ctx := cmd.Context()
			s, chans, err := a.channels(ctx, types)
			if err != nil {
				return err
			}
			items, err := a.scanUnread(ctx, s, chans)
			if err != nil {
				return err
			}
			type row struct {
				Channel core.Channel `json:"channel"`
				Count   int          `json:"count"`
				More    bool         `json:"more"` // 100개 넘게 남음
				Latest  core.Message `json:"latest"`
			}
			out := []row{}
			for _, it := range items {
				if it == nil {
					continue
				}
				out = append(out, row{it.ch, it.count, it.more, s.message(ctx, it.latest, it.ch.ID)})
			}
			slices.SortFunc(out, func(x, y row) int { return strings.Compare(y.Latest.TS, x.Latest.TS) })
			if a.asJSON {
				return a.printJSON(out)
			}
			for _, r := range out {
				name := "#" + r.Channel.Name
				if r.Channel.Kind == core.KindIM {
					name = "@" + s.userName(ctx, r.Channel.User)
				}
				n := fmt.Sprint(r.Count)
				if r.More {
					n += "+"
				}
				text := s.render(ctx, r.Latest.Text)
				fmt.Fprintf(a.out, "%-24s %4s  %s @%s: %s\n", name, n, core.Time(r.Latest.TS).Local().Format("01/02 15:04"),
					r.Latest.UserName, preview(text, 60))
			}
			return nil
		}),
	}
	cmd.Flags().StringSliceVar(&types, "type", nil, "public,private,im,mpim(기본 im,mpim)")
	cmd.Flags().BoolVar(&all, "all", false, "채널까지 전부(느리다)")
	return cmd
}

type unreadItem struct {
	ch     core.Channel
	count  int
	more   bool
	latest slack.Message
}

// scanUnread는 대화를 동시 8개로 훑는다. 첫 오류에서 나머지를 취소한다.
// 고루틴 안에서는 API만 부른다 — 이름 해석(session 캐시)은 끝난 뒤 한 고루틴에서.
func (a *app) scanUnread(ctx context.Context, s *session, chans []core.Channel) ([]*unreadItem, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	found := make([]*unreadItem, len(chans))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var done atomic.Int32
	progress := isTTY(a.errw)
	for i, c := range chans {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			it, err := s.unreadOne(ctx, c)
			if err != nil {
				cancel(err)
				return
			}
			found[i] = it
			if progress {
				fmt.Fprintf(a.errw, "\r확인 중 %d/%d", done.Add(1), len(chans))
			}
		})
	}
	wg.Wait()
	if progress {
		fmt.Fprint(a.errw, "\r\033[K")
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return found, nil // 안 읽은 게 없는 대화는 nil
}

// unreadOne은 last_read 이후 남이 쓴 메시지를 센다(최대 100개). 없으면 nil.
func (s *session) unreadOne(ctx context.Context, c core.Channel) (*unreadItem, error) {
	info, err := waitRateLimit(ctx, func() (*slack.Channel, error) {
		return s.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: c.ID})
	})
	var se slack.SlackErrorResponse
	if errors.As(err, &se) && (se.Err == "channel_not_found" || se.Err == "not_in_channel") {
		return nil, nil // 캐시가 오래됨
	}
	if err != nil {
		return nil, err
	}
	if c.Kind == core.KindIM && (info.Latest == nil || info.Latest.Timestamp <= info.LastRead) {
		return nil, nil // im은 info에 latest가 와서 history 호출을 아낀다
	}
	p := &slack.GetConversationHistoryParameters{ChannelID: c.ID, Oldest: info.LastRead, Limit: 100}
	if strings.Trim(p.Oldest, "0.") == "" {
		p.Oldest = "" // 한 번도 안 읽음("0000000000.000000") — Slack이 oldest로 받지 않는다
	}
	res, err := waitRateLimit(ctx, func() (*slack.GetConversationHistoryResponse, error) {
		return s.api.GetConversationHistoryContext(ctx, p)
	})
	if err != nil {
		return nil, err
	}
	it := &unreadItem{ch: c, more: res.HasMore}
	for _, m := range res.Messages { // 최신순
		if m.User == s.prof.UserID || m.SubType == "channel_join" || m.SubType == "channel_leave" {
			continue
		}
		if it.count == 0 {
			it.latest = m
		}
		it.count++
	}
	if it.count == 0 {
		return nil, nil
	}
	return it, nil
}

// waitRateLimit은 slack-go의 재시도(3회)가 끝난 뒤에도 429면 Retry-After만큼 기다렸다 다시 부른다.
// 팬아웃(unread)에서만 쓴다 — 다른 명령은 429가 계속되면 실패하는 게 맞다.
func waitRateLimit[T any](ctx context.Context, call func() (T, error)) (T, error) {
	for {
		v, err := call()
		var rl *slack.RateLimitedError
		if !errors.As(err, &rl) {
			return v, err
		}
		select {
		case <-time.After(rl.RetryAfter):
		case <-ctx.Done():
			return v, ctx.Err()
		}
	}
}
