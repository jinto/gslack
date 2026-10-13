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
	"github.com/slack-go/slack"
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
		&cobra.Command{
			Use:   "import [team]",
			Short: "Slack 데스크톱 앱(macOS)에서 session 프로필 가져오기 — 비공식",
			Long: `Slack 데스크톱 앱의 로컬 저장소에서 xoxc 토큰과 d 쿠키를 읽어 session 프로필을 만든다.
비공식(Slack 내부 형식 의존) — Slack 업데이트로 깨질 수 있다. macOS만.
팀이 여러 개면 [team](워크스페이스 이름 접두사)으로 고른다. Keychain 접근 승인 프롬프트가 뜰 수 있다.`,
			Args: cobra.MaximumNArgs(1),
			RunE: run(a.authImport),
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

	res, err := newAPI(sec.Token, sec.Cookie, apiTimeout).AuthTestContext(cmd.Context())
	if err != nil {
		return err
	}
	name, err := a.deriveProfileName(res)
	if err != nil {
		return err
	}
	if err := a.saveProfile(name, sec, res); err != nil {
		return err
	}
	if a.asJSON {
		return a.printJSON(map[string]any{"profile": name, "user": res.User, "team": res.Team})
	}
	fmt.Fprintf(a.out, "로그인: %s — %s @ %s\n", name, res.User, res.Team)
	return nil
}

// deriveProfileName은 --profile을 우선하고, 없으면 team URL 호스트 접두사를 쓴다.
func (a *app) deriveProfileName(res *slack.AuthTestResponse) (string, error) {
	name := a.profile
	if name == "" {
		name = hostPrefix(res.URL)
	}
	if name == "env" || !store.ValidProfileName(name) {
		return "", usagef("프로필 이름 %q를 쓸 수 없다 — --profile로 영문·숫자·._-만", name)
	}
	return name, nil
}

// saveProfile은 검증된 자격 증명을 저장하고 현재 프로필로 만든다(login·import 공통 꼬리).
func (a *app) saveProfile(name string, sec store.Secret, res *slack.AuthTestResponse) error {
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
	return cfg.Save()
}

// hostPrefix는 URL 호스트의 첫 레이블을 돌려준다(https://acme.slack.com → acme).
func hostPrefix(rawURL string) string {
	u, _ := url.Parse(rawURL)
	p, _, _ := strings.Cut(u.Hostname(), ".")
	return p
}

func (a *app) authImport(cmd *cobra.Command, args []string) error {
	teams, cookie, err := store.ImportSlackApp()
	if err != nil {
		return err
	}
	selector := "" // [team]은 워크스페이스를 고르고, --profile은 저장 이름만 바꾼다(둘을 섞지 않는다)
	if len(args) == 1 {
		selector = args[0]
	}
	team, err := pickTeam(teams, selector)
	if err != nil {
		return err
	}
	sec := store.Secret{Token: team.Token, Cookie: cookieHeader(cookie)}
	res, err := newAPI(sec.Token, sec.Cookie, apiTimeout).AuthTestContext(cmd.Context())
	if err != nil {
		return fmt.Errorf("가져온 자격 증명 검증 실패(만료됐으면 Slack에서 다시 로그인): %w", err)
	}
	name, err := a.deriveProfileName(res)
	if err != nil {
		return err
	}
	if err := a.saveProfile(name, sec, res); err != nil {
		return err
	}
	if a.asJSON {
		return a.printJSON(map[string]any{"profile": name, "user": res.User, "team": res.Team})
	}
	fmt.Fprintf(a.out, "가져옴: %s — %s @ %s\n", name, res.User, res.Team)
	return nil
}

// pickTeam은 selector(워크스페이스 이름/URL 접두사)로 팀을 고른다. 모호하면 후보를 나열한다.
func pickTeam(teams []store.SlackTeam, selector string) (store.SlackTeam, error) {
	if selector == "" {
		if len(teams) == 1 {
			return teams[0], nil
		}
		return store.SlackTeam{}, teamCandidates("팀이 여러 개다", teams)
	}
	sel := strings.ToLower(selector)
	var matches []store.SlackTeam
	for _, t := range teams {
		if strings.HasPrefix(hostPrefix(t.URL), sel) || strings.Contains(strings.ToLower(t.Name), sel) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return store.SlackTeam{}, usagef("팀 %q를 찾지 못했다", selector)
	default:
		return store.SlackTeam{}, teamCandidates(fmt.Sprintf("%q가 여러 팀과 맞는다", selector), matches)
	}
}

func teamCandidates(msg string, teams []store.SlackTeam) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — [team]으로 고른다:", msg)
	for _, t := range teams {
		fmt.Fprintf(&b, "\n  %s  %s", hostPrefix(t.URL), t.Name)
	}
	return usagef("%s", b.String())
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
