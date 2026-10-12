package cli

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
	"github.com/spf13/cobra"
)

func (a *app) searchCmd() *cobra.Command {
	var (
		n        int
		in, from string
	)
	cmd := &cobra.Command{
		Use:   "search <검색어>",
		Short: "메시지 검색 — 최신 n개를 오래된 것부터",
		Long: `search.messages(legacy API)라 Slack 앱 검색과 결과가 다를 수 있다.
검색어에 Slack 검색 문법(after:, has:, in:, from: …)을 그대로 쓸 수 있다.`,
		Args: cobra.ExactArgs(1),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			if n < 1 || n > 100 {
				return usagef("-n은 1~100")
			}
			ctx := cmd.Context()
			s, err := a.open(ctx)
			if err != nil {
				return err
			}
			q := args[0]
			if in != "" { // ID 형태가 이름 형태와 같은 결과를 낸다(2026-10-12 실측) — 이름 해석 오류가 없다
				tin, err := core.ParseTarget(in)
				if err != nil {
					return usageError{err}
				}
				if tin.Kind == core.TargetUserName || tin.Kind == core.TargetUserID {
					id, err := s.userID(ctx, tin, false)
					if err != nil {
						return err
					}
					q += " in:<@" + id + ">"
				} else {
					ch, err := s.channelID(ctx, tin)
					if err != nil {
						return err
					}
					q += " in:<#" + ch + ">"
				}
			}
			if from != "" {
				tfrom, err := core.ParseTarget(from)
				if err != nil {
					return usageError{err}
				}
				id, err := s.userID(ctx, tfrom, false)
				if err != nil {
					return err
				}
				q += " from:<@" + id + ">"
			}
			p := slack.NewSearchParameters()
			p.Sort, p.Count = "timestamp", n
			res, err := s.api.SearchMessagesContext(ctx, q, p)
			if err != nil {
				return err
			}
			out, where := []core.Message{}, []string{}
			for _, m := range slices.Backward(res.Matches) { // 최신순 → 오래된 것부터
				out = append(out, s.searchFrom(ctx, m))
				where = append(where, m.Channel.Name)
			}
			if a.asJSON {
				return a.printJSON(out)
			}
			if err := s.loadChannels(ctx, false); err != nil {
				return err
			}
			user := func(id string) string { return s.userName(ctx, id) }
			for i, m := range out {
				ch := "#" + where[i]
				if strings.HasPrefix(m.Channel, "D") { // DM은 이름 자리에 상대 ID가 온다
					ch = "@" + s.userName(ctx, where[i])
				}
				fmt.Fprintf(a.out, "%-17s  %s  %s  @%s: %s\n", m.TS, core.Time(m.TS).Local().Format("01/02 15:04"), ch, m.UserName,
					preview(core.Render(m.Text, user, s.channelName), 80))
			}
			return nil
		}),
	}
	f := cmd.Flags()
	f.IntVarP(&n, "limit", "n", 20, "결과 수(1~100)")
	f.StringVar(&in, "in", "", "대화(#채널, @사용자 DM, ID)")
	f.StringVar(&from, "from", "", "보낸 사람(@이름 또는 ID)")
	return cmd
}

// searchFrom은 검색 결과를 core.Message로. permalink는 API 값, thread_ts는 permalink에서.
func (s *session) searchFrom(ctx context.Context, m slack.SearchMessage) core.Message {
	out := core.Message{Channel: m.Channel.ID, TS: m.Timestamp, User: m.User, Text: m.Text, Permalink: m.Permalink,
		UserName: cmp.Or(m.Username, m.User), Bot: m.User == ""}
	if m.User != "" {
		out.UserName = s.userName(ctx, m.User)
	}
	if t, err := core.ParseTarget(m.Permalink); err == nil {
		out.ThreadTS = t.ThreadTS
	}
	return out
}
