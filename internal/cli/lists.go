package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jinto/gslack/internal/core"
	"github.com/spf13/cobra"
)

func (a *app) channelsCmd() *cobra.Command {
	var types []string
	cmd := &cobra.Command{
		Use:   "channels",
		Short: "가입한 대화 목록",
		Args:  cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, out, err := a.channels(ctx, types)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(out)
			}
			for _, c := range out {
				name := "#" + c.Name
				if c.Kind == core.KindIM {
					name = "@" + s.userName(ctx, c.User)
				}
				fmt.Fprintf(a.out, "%-8s %-12s %s\n", c.Kind, c.ID, name)
			}
			return nil
		}),
	}
	cmd.Flags().StringSliceVar(&types, "type", nil, "public,private,im,mpim")
	return cmd
}

// channels는 --type으로 거른 가입 대화 목록이다.
func (a *app) channels(ctx context.Context, types []string) (*session, []core.Channel, error) {
	for _, t := range types {
		if !slices.Contains([]string{core.KindPublic, core.KindPrivate, core.KindIM, core.KindMPIM}, t) {
			return nil, nil, usagef("--type은 public,private,im,mpim 중에서")
		}
	}
	s, err := a.open(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := s.loadChannels(ctx, false); err != nil {
		return nil, nil, err
	}
	out := []core.Channel{}
	for _, c := range s.chans {
		if len(types) == 0 || slices.Contains(types, c.Kind) {
			out = append(out, c)
		}
	}
	return s, out, nil
}

func (a *app) usersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "users [query]",
		Short: "사용자 검색(이름·실명·표시명 부분 일치)",
		Args:  cobra.MaximumNArgs(1),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			s, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			if err := s.loadUsers(cmd.Context(), false); err != nil {
				return err
			}
			q := ""
			if len(args) == 1 {
				q = strings.ToLower(strings.TrimPrefix(args[0], "@"))
			}
			out := []core.User{}
			for _, u := range s.users {
				if u.Deleted || u.Name == "" {
					continue
				}
				if q == "" || strings.Contains(strings.ToLower(u.Name+"\x00"+u.RealName+"\x00"+u.DisplayName), q) {
					out = append(out, u)
				}
			}
			slices.SortFunc(out, func(x, y core.User) int { return strings.Compare(x.Name, y.Name) })
			if a.asJSON {
				return a.printJSON(out)
			}
			for _, u := range out {
				fmt.Fprintf(a.out, "%-12s @%-20s %s\n", u.ID, u.Name, u.RealName)
			}
			return nil
		}),
	}
}

func (a *app) whoisCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whois <@user|U…>",
		Short: "사용자 정보와 프레즌스",
		Args:  cobra.ExactArgs(1),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			t, err := core.ParseTarget(args[0])
			if err != nil {
				return usageError{err}
			}
			if t.Kind == core.TargetChannelID || t.Kind == core.TargetPermalink { // 맨 이름(`whois jaekwon`)은 받는다
				return usagef("사용자(@이름 또는 U…/W… ID)를 지정하라")
			}
			ctx := cmd.Context()
			s, err := a.open(ctx)
			if err != nil {
				return err
			}
			id, err := s.userID(ctx, t, false)
			if err != nil {
				return err
			}
			su, err := s.api.GetUserInfoContext(ctx, id)
			if err != nil {
				return err
			}
			p, err := s.api.GetUserPresenceContext(ctx, id)
			if err != nil {
				return err
			}
			out := struct {
				core.User
				Presence string `json:"presence"`
			}{userFrom(*su), p.Presence}
			if a.asJSON {
				return a.printJSON(out)
			}
			fmt.Fprintf(a.out, "%s @%s — %s\n", out.ID, out.Name, out.RealName)
			for _, kv := range [][2]string{{"표시명", out.DisplayName}, {"직함", out.Title},
				{"상태", strings.TrimSpace(core.Emoji(out.StatusEmoji) + " " + out.StatusText)}, {"시간대", out.TZ}, {"프레즌스", out.Presence}} {
				if kv[1] != "" {
					fmt.Fprintf(a.out, "  %-6s %s\n", kv[0], kv[1])
				}
			}
			return nil
		}),
	}
}
