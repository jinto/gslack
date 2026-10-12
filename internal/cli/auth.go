package cli

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/jinto/gslack/internal/store"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *app) authCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "로그인과 프로필"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "login",
			Short: "토큰을 검증하고 프로필로 저장(토큰: GSLACK_TOKEN, stdin, 또는 프롬프트)",
			Long: `토큰은 인자로 받지 않는다(셸 히스토리·ps에 남는다).
입력 순서: 환경 변수 → stdin(파이프면 한 줄씩) → 터미널 프롬프트.
  1. 토큰: GSLACK_TOKEN (xoxp-… 또는 xoxc-…)
  2. 쿠키(xoxc만): GSLACK_COOKIE (xoxd-… 또는 raw Cookie 헤더 "d=…; d-s=…")
  3. 앱 토큰(선택, watch용): GSLACK_APP_TOKEN (xapp-…)`,
			Args: cobra.NoArgs,
			RunE: run(a.login),
		},
		&cobra.Command{Use: "status", Short: "현재 프로필로 auth.test", Args: cobra.NoArgs, RunE: run(a.authStatus)},
		&cobra.Command{Use: "list", Short: "프로필 목록", Args: cobra.NoArgs, RunE: run(a.authList)},
		&cobra.Command{Use: "use <profile>", Short: "기본 프로필 바꾸기", Args: cobra.ExactArgs(1), RunE: run(a.authUse)},
		&cobra.Command{Use: "logout [profile]", Short: "자격 증명·캐시 삭제", Args: cobra.MaximumNArgs(1), RunE: run(a.authLogout)},
	)
	return cmd
}

func (a *app) login(cmd *cobra.Command, _ []string) error {
	r, tty := bufio.NewReader(a.in), isTTY(a.in)
	// value는 환경 변수 → 터미널 비밀 입력 → stdin 줄 순으로 값을 받는다.
	value := func(env, prompt string) (string, error) {
		if v := os.Getenv(env); v != "" {
			return strings.TrimSpace(v), nil
		}
		if tty {
			fmt.Fprint(a.errw, prompt)
			b, err := term.ReadPassword(int(a.in.(*os.File).Fd()))
			fmt.Fprintln(a.errw)
			return strings.TrimSpace(string(b)), err
		}
		line, err := r.ReadString('\n')
		if err == io.EOF {
			err = nil
		}
		return strings.TrimSpace(line), err
	}
	tok, err := value("GSLACK_TOKEN", "Token (xoxp-… 또는 xoxc-…): ")
	if err != nil {
		return err
	}
	sec := store.Secret{Token: tok}
	switch {
	case strings.HasPrefix(tok, "xoxp-"):
	case strings.HasPrefix(tok, "xoxc-"):
		c, err := value("GSLACK_COOKIE", "Cookie (xoxd-… 또는 d=…; d-s=…): ")
		if err != nil {
			return err
		}
		if sec.Cookie = cookieHeader(c); sec.Cookie == "" {
			return usagef("xoxc 토큰에는 d 쿠키가 필요하다")
		}
	case tok == "":
		return usagef("토큰이 비었다")
	default:
		return usagef("xoxp- 또는 xoxc- 토큰만 받는다")
	}
	if sec.AppToken, err = value("GSLACK_APP_TOKEN", "App token (xapp-…, 없으면 Enter): "); err != nil {
		return err
	}
	if sec.AppToken != "" && !strings.HasPrefix(sec.AppToken, "xapp-") {
		return usagef("앱 토큰은 xapp-로 시작한다")
	}

	res, err := newAPI(sec.Token, sec.Cookie).AuthTestContext(cmd.Context())
	if err != nil {
		return err
	}
	name := a.profile
	if name == "" {
		u, _ := url.Parse(res.URL)
		name, _, _ = strings.Cut(u.Hostname(), ".")
	}
	if name == "env" || !store.ValidProfileName(name) {
		return usagef("프로필 이름 %q를 쓸 수 없다 — --profile로 영문·숫자·._-만", name)
	}
	if err := store.NewSecretStore().Set(name, sec); err != nil {
		return err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	cfg.Profiles[name] = profileFrom(res, sec.Token)
	_ = store.OpenCache(name).Clear() // 같은 이름으로 다른 워크스페이스에 로그인할 수 있다
	cfg.Current = name
	if err := cfg.Save(); err != nil {
		return err
	}
	if a.asJSON {
		return a.printJSON(map[string]any{"profile": name, "user": res.User, "team": res.Team})
	}
	fmt.Fprintf(a.out, "로그인: %s — %s @ %s\n", name, res.User, res.Team)
	return nil
}

func (a *app) authStatus(cmd *cobra.Command, _ []string) error {
	s, err := a.open(cmd.Context())
	if err != nil {
		return err
	}
	res, err := s.api.AuthTestContext(cmd.Context())
	if err != nil {
		return err
	}
	if a.asJSON {
		return a.printJSON(map[string]any{"profile": s.name, "kind": s.prof.Kind, "user": res.User,
			"user_id": res.UserID, "team": res.Team, "team_id": res.TeamID, "url": res.URL})
	}
	fmt.Fprintf(a.out, "%s(%s) — %s(%s) @ %s %s\n", s.name, s.prof.Kind, res.User, res.UserID, res.Team, res.URL)
	return nil
}

func (a *app) authList(*cobra.Command, []string) error {
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(cfg.Profiles))
	for n := range cfg.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	if a.asJSON {
		out := []map[string]any{}
		for _, n := range names {
			p := cfg.Profiles[n]
			out = append(out, map[string]any{"profile": n, "current": n == cfg.Current, "kind": p.Kind,
				"user": p.User, "team": p.Team, "url": p.URL})
		}
		return a.printJSON(out)
	}
	for _, n := range names {
		mark := " "
		if n == cfg.Current {
			mark = "*"
		}
		p := cfg.Profiles[n]
		fmt.Fprintf(a.out, "%s %-12s %-8s %s @ %s\n", mark, n, p.Kind, p.User, p.Team)
	}
	return nil
}

func (a *app) authUse(_ *cobra.Command, args []string) error {
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Profiles[args[0]]; !ok {
		return usagef("프로필 %q가 없다", args[0])
	}
	cfg.Current = args[0]
	return cfg.Save()
}

func (a *app) authLogout(_ *cobra.Command, args []string) error {
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	name := cfg.Current
	if a.profile != "" {
		name = a.profile
	}
	if len(args) == 1 {
		name = args[0]
	}
	if _, ok := cfg.Profiles[name]; !ok || !store.ValidProfileName(name) {
		return usagef("프로필 %q가 없다", name)
	}
	if err := store.NewSecretStore().Delete(name); err != nil {
		return err
	}
	delete(cfg.Profiles, name)
	if cfg.Current == name {
		cfg.Current = ""
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	return store.OpenCache(name).Clear()
}
