package cli

import (
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/jinto/gslack/internal/core"
	"github.com/jinto/gslack/internal/store"
	"github.com/spf13/cobra"
)

// 셸 자동완성. 탭마다 프로세스가 새로 뜨므로 네트워크를 쓰지 않고 이름 캐시 파일만 읽는다(만료돼도 쓴다).
// 채널 후보는 `#` 없는 맨 이름이다 — cobra 스크립트가 eval로 부르면 `#…`는 주석이 돼 사라진다.

func (a *app) addCompletions(root *cobra.Command) {
	root.RegisterFlagCompletionFunc("profile", a.completeProfiles)
	for _, c := range root.Commands() {
		switch c.Name() {
		case "read", "send", "mark", "thread", "react", "edit", "delete", "download":
			c.ValidArgsFunction = firstArg(a.completeTargets, cobra.ShellCompDirectiveNoFileComp)
		case "upload":
			c.ValidArgsFunction = firstArg(a.completeTargets, cobra.ShellCompDirectiveDefault) // 둘째는 파일
		case "watch":
			c.ValidArgsFunction = a.completeTargets
		case "whois":
			c.ValidArgsFunction = firstArg(a.completeUsers, cobra.ShellCompDirectiveNoFileComp)
		case "search":
			c.RegisterFlagCompletionFunc("in", a.completeTargets)
			c.RegisterFlagCompletionFunc("from", a.completeUsers)
		case "auth":
			for _, sub := range c.Commands() {
				switch sub.Name() {
				case "use", "logout":
					sub.ValidArgsFunction = firstArg(a.completeProfiles, cobra.ShellCompDirectiveNoFileComp)
				default: // import의 [team]은 Slack 앱에서만 온다 — 파일 완성만 끈다
					sub.ValidArgsFunction = cobra.NoFileCompletions
				}
			}
		default: // 파일을 받는 명령이 아니다
			c.ValidArgsFunction = cobra.NoFileCompletions
		}
	}
}

// firstArg는 첫 인자만 완성한다(cobra는 완성 때 Args 검사를 하지 않는다). 그 뒤는 rest.
func firstArg(f cobra.CompletionFunc, rest cobra.ShellCompDirective) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, rest
		}
		return f(cmd, args, prefix)
	}
}

// cache는 완성에 쓸 프로필 캐시다. env 모드는 캐시 경로에 team/user가 필요해 완성하지 않는다.
func (a *app) cache() (store.Cache, bool) {
	if os.Getenv("GSLACK_TOKEN") != "" {
		return store.Cache{}, false
	}
	name := a.profile
	if name == "" {
		cfg, err := store.LoadConfig()
		if err != nil {
			return store.Cache{}, false
		}
		name = cfg.Current
	}
	if !store.ValidProfileName(name) {
		return store.Cache{}, false
	}
	return store.OpenCache(name), true
}

func (a *app) completeTargets(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if strings.HasPrefix(prefix, "@") {
		return a.completeUsers(cmd, args, prefix)
	}
	c, ok := a.cache()
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	chans, _ := store.LoadCache[core.Channel](c, "channels", 0)
	var out []string
	for _, ch := range chans {
		if (ch.Kind == core.KindPublic || ch.Kind == core.KindPrivate) && strings.HasPrefix(ch.Name, prefix) {
			out = append(out, ch.Name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeUsers는 `@handle`만 낸다(셸이 입력 접두사로 다시 거르므로 표시명 매칭은 못 한다). 표시명은 설명 칸에.
func (a *app) completeUsers(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	c, ok := a.cache()
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	users, _ := store.LoadCache[core.User](c, "users", 0)
	var out []string
	for _, u := range users {
		if h := "@" + u.Name; !u.Deleted && strings.HasPrefix(h, prefix) {
			out = append(out, h+"\t"+displayName(u, u.Name))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func (a *app) completeProfiles(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(cfg.Profiles)) {
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
