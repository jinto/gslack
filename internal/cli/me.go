package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) statusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "내 상태(인자 없으면 조회)",
		Args:  cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, _ []string) error {
			s, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			return a.showStatus(cmd.Context(), s)
		}),
	}
	var emoji, until string
	set := &cobra.Command{
		Use:   "set <text>",
		Short: "상태 설정(--emoji가 없으면 Slack 기본 :speech_balloon:)",
		Args:  cobra.ExactArgs(1),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return usagef("상태 문구가 비었다 — 지우려면 status clear")
			}
			var exp int64
			if until != "" {
				t, err := parseWhen(until, time.Now(), 1)
				if err != nil {
					return usagef("--until %v", err)
				}
				if !t.After(time.Now()) {
					return usagef("--until은 미래여야 한다")
				}
				exp = t.Unix()
			}
			if emoji = strings.Trim(emoji, ":"); emoji != "" {
				emoji = ":" + emoji + ":"
			}
			s, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			if err := s.api.SetUserCustomStatusContext(cmd.Context(), args[0], emoji, exp); err != nil {
				return err
			}
			a.showStatusAfter(cmd.Context(), s)
			return nil
		}),
	}
	set.Flags().StringVar(&emoji, "emoji", "", ":emoji:")
	set.Flags().StringVar(&until, "until", "", "만료(2h, 1d 또는 RFC3339). 없으면 계속")
	clear := &cobra.Command{
		Use:   "clear",
		Short: "상태 지우기",
		Args:  cobra.NoArgs,
		RunE: run(func(cmd *cobra.Command, _ []string) error {
			s, err := a.open(cmd.Context())
			if err != nil {
				return err
			}
			if err := s.api.UnsetUserCustomStatusContext(cmd.Context()); err != nil {
				return err
			}
			a.showStatusAfter(cmd.Context(), s)
			return nil
		}),
	}
	cmd.AddCommand(set, clear)
	return cmd
}

// showStatusAfter: 바꾸기는 이미 성공했다 — 다시 조회하다 실패해도 경고만(재시도하면 --until이 늘어난다).
func (a *app) showStatusAfter(ctx context.Context, s *session) {
	if err := a.showStatus(ctx, s); err != nil {
		fmt.Fprintln(a.errw, "gslack: 상태는 바꿨지만 다시 조회하지 못했다:", err)
	}
}

// showStatus는 users.info로 지금 상태를 보여준다(이름 캐시는 24시간 묵으니 쓰지 않는다).
func (a *app) showStatus(ctx context.Context, s *session) error {
	u, err := s.api.GetUserInfoContext(ctx, s.prof.UserID)
	if err != nil {
		return err
	}
	p := u.Profile
	if a.asJSON {
		return a.printJSON(map[string]any{"status_text": p.StatusText, "status_emoji": p.StatusEmoji, "status_expiration": p.StatusExpiration})
	}
	if p.StatusText == "" && p.StatusEmoji == "" {
		fmt.Fprintln(a.out, "상태 없음")
		return nil
	}
	line := strings.TrimSpace(p.StatusEmoji + " " + p.StatusText)
	if p.StatusExpiration > 0 {
		line += time.Unix(int64(p.StatusExpiration), 0).Local().Format(" (~01/02 15:04)")
	}
	fmt.Fprintln(a.out, line)
	return nil
}
