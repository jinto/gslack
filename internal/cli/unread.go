package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
한 번도 읽지 않은 채널은 대개 100+로 나온다.
session(xoxc) 프로필은 비공식 client.counts 한 번으로 안 읽은 대화만 골라 빠르다. 다만 Slack이 최근 활동
대화만 돌려줘서 오래 조용한 채널은 빠진다. client.counts가 실패하면 위의 느린 방식으로.`,
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
			items, err := a.unreadRows(ctx, s, chans)
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
				out = append(out, row{it.ch, it.count, it.more, s.message(ctx, it.latest, it.ch.ID)})
			}
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

// unreadRows는 안 읽은 대화를 최신순으로 돌려준다. session은 client.counts로 대상을 줄이고, 실패하면 경고 뒤 전부 훑는다.
// 이름 해석(session 캐시)은 하지 않는다 — tui가 Cmd 고루틴에서 부른다.
func (a *app) unreadRows(ctx context.Context, s *session, chans []core.Channel) ([]*unreadItem, error) {
	if s.prof.Kind == "session" {
		ids, err := s.clientCounts(ctx)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			fmt.Fprintf(a.errw, "client.counts 실패(%v) — 대화마다 확인한다(느림)\n", err)
		default:
			chans = slices.DeleteFunc(slices.Clone(chans), func(c core.Channel) bool { return !ids[c.ID] }) // tui가 chans를 다시 쓴다
		}
	}
	items, err := a.scanUnread(ctx, s, chans)
	if err != nil {
		return nil, err
	}
	items = slices.DeleteFunc(items, func(it *unreadItem) bool { return it == nil })
	slices.SortFunc(items, func(x, y *unreadItem) int { return strings.Compare(y.latest.Timestamp, x.latest.Timestamp) })
	return items, nil
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

// clientCounts는 비공식 client.counts로 안 읽은 대화 ID를 받는다(session 토큰만). 응답에 없는 대화는 읽음으로 본다.
func (s *session) clientCounts(ctx context.Context) (map[string]bool, error) {
	base := cmp.Or(apiURL, strings.TrimRight(s.prof.URL, "/")+"/api/")
	req, err := http.NewRequestWithContext(ctx, "POST", base+"client.counts", strings.NewReader(url.Values{"token": {s.token}}.Encode()))
	if err != nil {
		return nil, err
	}
	if apiURL == "" && !isSlackURL(req.URL) {
		return nil, fmt.Errorf("Slack 주소가 아니다: %q", s.prof.URL) // 토큰을 실어 보내므로
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	type entry struct {
		ID         string `json:"id"`
		HasUnreads bool   `json:"has_unreads"`
	}
	var body struct {
		OK                   bool
		Error                string
		Channels, MPIMs, IMs []entry
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	if !body.OK {
		return nil, errors.New(cmp.Or(body.Error, "ok=false"))
	}
	ids := map[string]bool{}
	for _, e := range slices.Concat(body.Channels, body.MPIMs, body.IMs) {
		if e.HasUnreads {
			ids[e.ID] = true
		}
	}
	return ids, nil
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
