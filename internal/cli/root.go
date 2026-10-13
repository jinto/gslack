// Package cli는 cobra 명령, Slack 클라이언트 세션, 출력을 담당한다.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/slack-go/slack"
	"github.com/spf13/cobra"
)

type app struct {
	in        io.Reader
	out, errw io.Writer

	profile              string
	asJSON, raw, refresh bool
}

// Execute는 명령을 실행하고 종료 코드를 돌려준다: 0 성공, 1 API/인증/IO 오류, 2 사용법 오류.
func Execute(ctx context.Context, args []string, in io.Reader, out, errw io.Writer) int {
	a := &app{in: in, out: out, errw: errw}
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errw)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	// 플래그 파싱이 실패하면 --json도 아직 안 읽혔다.
	a.asJSON = a.asJSON || slices.ContainsFunc(args, func(s string) bool { return s == "--json" || s == "--json=true" })
	a.printError(err)
	return exitCode(err)
}

// Version은 릴리스 빌드가 -ldflags "-X github.com/jinto/gslack/internal/cli.Version=…"로 넣는다.
var Version = "dev"

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gslack",
		Version:       Version,
		Short:         "터미널에서 쓰는 Slack",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if a.asJSON && a.raw {
				return usagef("--json과 --raw는 함께 쓸 수 없다")
			}
			if a.raw && !slices.Contains([]string{"read", "thread"}, cmd.Name()) {
				return usagef("--raw는 read·thread에서만 쓴다")
			}
			return nil
		},
	}
	f := root.PersistentFlags()
	f.StringVar(&a.profile, "profile", os.Getenv("GSLACK_PROFILE"), "사용할 프로필(기본: 현재 프로필)")
	f.BoolVar(&a.asJSON, "json", false, "정규화된 JSON으로 출력")
	f.BoolVar(&a.raw, "raw", false, "Slack 원본 JSON으로 출력(read·thread만)")
	f.BoolVar(&a.refresh, "refresh", false, "이름 캐시를 무시하고 새로 받기")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.AddCommand(a.authCmd(), a.channelsCmd(), a.usersCmd(), a.whoisCmd(), a.readCmd(),
		a.threadCmd(), a.sendCmd(), a.markCmd(), a.unreadCmd(),
		a.searchCmd(), a.reactCmd(), a.editCmd(), a.deleteCmd(), a.statusCmd(),
		a.uploadCmd(), a.downloadCmd(), a.watchCmd(), a.tuiCmd())
	a.addCompletions(root)
	return root
}

// run은 RunE를 감싸서, 명령 실행 중 난 오류와 cobra 자체 오류(인자·플래그·알 수 없는 명령)를 구분한다.
func run(f func(cmd *cobra.Command, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := f(cmd, args); err != nil {
			var u usageError
			if errors.As(err, &u) {
				return err
			}
			return runError{err}
		}
		return nil
	}
}

type usageError struct{ error }
type runError struct{ error }

func (e usageError) Unwrap() error { return e.error }
func (e runError) Unwrap() error   { return e.error }

func usagef(format string, args ...any) error { return usageError{fmt.Errorf(format, args...)} }

var errNotLoggedIn = errors.New("로그인된 프로필이 없다 — `gslack auth login`")

func exitCode(err error) int {
	var r runError
	if errors.As(err, &r) {
		return 1
	}
	return 2 // usageError 또는 cobra의 인자·명령 오류
}

func errorCode(err error) string {
	var (
		se slack.SlackErrorResponse
		rl *slack.RateLimitedError
		sc slack.StatusCodeError
	)
	switch {
	case errors.Is(err, errNotLoggedIn):
		return "not_logged_in"
	case errors.As(err, &se):
		return se.Err
	case errors.As(err, &rl):
		return "rate_limited"
	case errors.As(err, &sc):
		return fmt.Sprintf("http_%d", sc.Code)
	case errors.Is(err, context.Canceled):
		return "canceled"
	case exitCode(err) == 2:
		return "usage"
	}
	return "internal"
}

func (a *app) printError(err error) {
	code, msg := errorCode(err), err.Error()
	switch code {
	case "invalid_auth", "token_revoked", "not_authed", "account_inactive":
		msg += " — 토큰이 만료됐을 수 있다. `gslack auth login --profile <이름>`(session은 `gslack auth import`)으로 다시 로그인"
	}
	if a.asJSON {
		enc := json.NewEncoder(a.errw)
		enc.SetEscapeHTML(false)
		enc.Encode(map[string]string{"error": code, "message": msg})
		return
	}
	fmt.Fprintln(a.errw, "gslack:", strings.TrimSpace(msg))
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
