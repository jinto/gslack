package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jinto/gslack/internal/core"
	"github.com/jinto/gslack/internal/store"
	"github.com/slack-go/slack"
)

// session은 한 명령 동안 쓰는 Slack 클라이언트와 이름 캐시다.
type session struct {
	name    string
	prof    store.Profile
	api     *slack.Client
	cache   store.Cache
	refresh bool

	users      map[string]core.User // id → user
	usersFresh bool
	chans      []core.Channel
	chansFresh bool
}

func newAPI(token, cookie string) *slack.Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	if cookie != "" {
		hc.Transport = cookieTransport{cookie: cookie, next: http.DefaultTransport}
	}
	rc := slack.DefaultRetryConfig()
	rc.RetryAfterDuration = 5 * time.Second                                         // Retry-After가 없을 때 기본 60s 멈춤 방지
	opts := []slack.Option{slack.OptionHTTPClient(hc), slack.OptionRetryConfig(rc)} // 429만 재시도
	if apiURL != "" {
		opts = append(opts, slack.OptionAPIURL(apiURL))
	}
	return slack.New(token, opts...)
}

var apiURL string // 테스트에서 가짜 서버로 바꾼다

// cookieTransport는 xoxc 세션의 d 쿠키를 Slack 호스트로 가는 요청에만 붙인다.
type cookieTransport struct {
	cookie string
	next   http.RoundTripper
}

func (t cookieTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if h := req.URL.Hostname(); req.URL.Scheme == "https" && (h == "slack.com" || strings.HasSuffix(h, ".slack.com")) {
		req = req.Clone(req.Context())
		req.Header.Set("Cookie", t.cookie)
	}
	return t.next.RoundTrip(req)
}

// open은 GSLACK_TOKEN(있으면) 또는 프로필로 세션을 연다.
func (a *app) open(ctx context.Context) (*session, error) {
	if a.profile != "" && !store.ValidProfileName(a.profile) {
		return nil, usagef("프로필 이름 %q가 올바르지 않다", a.profile)
	}
	if tok := os.Getenv("GSLACK_TOKEN"); tok != "" {
		api := newAPI(tok, cookieHeader(os.Getenv("GSLACK_COOKIE")))
		res, err := api.AuthTestContext(ctx)
		if err != nil {
			return nil, err
		}
		return &session{name: "env", prof: profileFrom(res, tok), api: api,
			cache: store.OpenCache("env-" + res.TeamID + "-" + res.UserID), refresh: a.refresh}, nil
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return nil, err
	}
	name := a.profile
	if name == "" {
		name = cfg.Current
	}
	prof, ok := cfg.Profiles[name]
	if !ok {
		if name != "" {
			return nil, usagef("프로필 %q가 없다 — `gslack auth list`", name)
		}
		return nil, errNotLoggedIn
	}
	sec, err := store.NewSecretStore().Get(name)
	if errors.Is(err, store.ErrNoSecret) {
		return nil, errNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	return &session{name: name, prof: prof, api: newAPI(sec.Token, sec.Cookie),
		cache: store.OpenCache(name), refresh: a.refresh}, nil
}

func profileFrom(res *slack.AuthTestResponse, token string) store.Profile {
	kind := "user"
	if strings.HasPrefix(token, "xoxc-") {
		kind = "session"
	}
	return store.Profile{Kind: kind, Team: res.Team, TeamID: res.TeamID, User: res.User, UserID: res.UserID, URL: res.URL}
}

// cookieHeader는 xoxd 값만 받으면 `d=`를 붙인다. raw Cookie 헤더(d=…; d-s=…)는 그대로.
func cookieHeader(c string) string {
	c = strings.TrimSpace(c)
	if strings.HasPrefix(c, "xoxd-") {
		return "d=" + c
	}
	return c
}

// --- 사용자 ---

func (s *session) loadUsers(ctx context.Context, force bool) error {
	if s.users != nil && !force {
		return nil
	}
	items, ok := store.LoadCache[core.User](s.cache, "users")
	if !ok || s.refresh || force {
		// GetUsersContext는 429를 횟수 제한 없이 재시도한다 — 페이지를 직접 돌리고 오류는 그대로 낸다.
		items = nil
		p := s.api.GetUsersPaginated(slack.GetUsersOptionLimit(200))
		for {
			var err error
			if p, err = p.Next(ctx); p.Done(err) {
				break
			} else if err != nil {
				return err
			}
			for _, u := range p.Users {
				items = append(items, userFrom(u))
			}
		}
		s.usersFresh = true
		_ = store.SaveCache(s.cache, "users", items) // 캐시 저장 실패는 다음 실행이 느려질 뿐이다
	}
	s.users = make(map[string]core.User, len(items))
	for _, u := range items {
		s.users[u.ID] = u
	}
	return nil
}

// user는 ID로 찾고, 캐시에 없으면(예: Slack Connect 외부 사용자) users.info로 받는다.
// 폴백 결과는 메모리에만 둔다 — 파일에 쓰면 users.json의 fetched_at이 갱신돼 TTL이 안 끝난다.
func (s *session) user(ctx context.Context, id string) (core.User, error) {
	if err := s.loadUsers(ctx, false); err != nil {
		return core.User{}, err
	}
	if u, ok := s.users[id]; ok {
		return u, nil
	}
	su, err := s.api.GetUserInfoContext(ctx, id)
	var se slack.SlackErrorResponse
	if errors.As(err, &se) {
		s.users[id] = core.User{ID: id} // user_not_found 등 확정 실패만 기억한다(429·네트워크는 다시 시도)
	}
	if err != nil {
		return core.User{}, err
	}
	u := userFrom(*su)
	s.users[id] = u
	return u, nil
}

// userName은 화면에 보일 이름. 못 찾으면 ID.
func (s *session) userName(ctx context.Context, id string) string {
	u, _ := s.user(ctx, id)
	return displayName(u, id)
}

func displayName(u core.User, fallback string) string {
	for _, n := range []string{u.DisplayName, u.RealName, u.Name} {
		if n != "" {
			return n
		}
	}
	return fallback
}

// findUser는 handle → display_name → real_name 순서로 대소문자 무시 일치를 찾고,
// exact가 아니면 세 필드의 부분 일치로도 찾는다(`@민수` → display_name "minsu (민수)").
func (s *session) findUser(ctx context.Context, name string, exact bool) (core.User, error) {
	if err := s.loadUsers(ctx, false); err != nil {
		return core.User{}, err
	}
	hits := s.matchUsers(name, exact)
	if len(hits) == 0 && !s.usersFresh {
		if err := s.loadUsers(ctx, true); err != nil {
			return core.User{}, err
		}
		hits = s.matchUsers(name, exact)
	}
	switch len(hits) {
	case 0:
		if exact {
			return core.User{}, usagef("@%s와 정확히 일치하는 사람이 없다 — `gslack users %s`로 찾아 정확한 이름이나 ID로", name, name)
		}
		return core.User{}, usagef("사용자 @%s를 찾을 수 없다", name)
	case 1:
		return hits[0], nil
	}
	var c []string
	for _, u := range hits {
		c = append(c, fmt.Sprintf("%s(@%s %s)", u.ID, u.Name, displayName(u, u.Name)))
	}
	slices.Sort(c)
	return core.User{}, usagef("@%s가 여러 명이다: %s — ID로 지정하라", name, strings.Join(c, ", "))
}

func (s *session) matchUsers(name string, exact bool) []core.User {
	q := strings.ToLower(name)
	stages := []func(core.User) bool{
		func(u core.User) bool { return strings.EqualFold(u.Name, name) },
		func(u core.User) bool { return strings.EqualFold(u.DisplayName, name) },
		func(u core.User) bool { return strings.EqualFold(u.RealName, name) },
		func(u core.User) bool {
			return strings.Contains(strings.ToLower(u.Name+"\x00"+u.DisplayName+"\x00"+u.RealName), q)
		},
	}
	if exact {
		stages = stages[:3]
	}
	for _, match := range stages {
		var hits []core.User
		for _, u := range s.users {
			if !u.Deleted && match(u) {
				hits = append(hits, u)
			}
		}
		if len(hits) > 0 {
			return hits
		}
	}
	return nil
}

// --- 대화 ---

func (s *session) loadChannels(ctx context.Context, force bool) error {
	if s.chans != nil && !force {
		return nil
	}
	items, ok := store.LoadCache[core.Channel](s.cache, "channels")
	if !ok || s.refresh || force {
		items = nil
		p := &slack.GetConversationsForUserParameters{
			Types: []string{"public_channel", "private_channel", "im", "mpim"}, Limit: 200, ExcludeArchived: true}
		for {
			cs, next, err := s.api.GetConversationsForUserContext(ctx, p)
			if err != nil {
				return err
			}
			for _, c := range cs {
				items = append(items, channelFrom(c))
			}
			if next == "" {
				break
			}
			p.Cursor = next
		}
		s.chansFresh = true
		_ = store.SaveCache(s.cache, "channels", items)
	}
	s.chans = items
	return nil
}

func (s *session) channelName(id string) string {
	for _, c := range s.chans {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// channelID는 target을 대화 ID로 바꾼다. 캐시에서 못 찾으면 한 번 새로 받아 다시 찾는다.
func (s *session) channelID(ctx context.Context, t core.Target) (string, error) {
	switch t.Kind {
	case core.TargetChannelID, core.TargetPermalink:
		return t.ID, nil
	}
	userID := ""
	if t.Kind != core.TargetChannelName {
		var err error
		if userID, err = s.userID(ctx, t, false); err != nil {
			return "", err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.loadChannels(ctx, attempt == 1); err != nil {
			return "", err
		}
		for _, c := range s.chans {
			if t.Kind == core.TargetChannelName && c.Kind != core.KindIM && strings.EqualFold(c.Name, t.Name) ||
				userID != "" && c.Kind == core.KindIM && c.User == userID {
				return c.ID, nil
			}
		}
		if s.chansFresh {
			break
		}
	}
	if t.Kind == core.TargetChannelName {
		return "", usagef("#%s를 찾을 수 없다(가입한 채널만 대상)", t.Name)
	}
	return "", usageError{noDMError{userID, cmp.Or(t.Name, userID)}}
}

// userID는 @이름, 맨 이름, U…/W… target을 사용자 ID로 바꾼다. exact면 부분 일치를 받지 않는다.
func (s *session) userID(ctx context.Context, t core.Target, exact bool) (string, error) {
	switch t.Kind {
	case core.TargetUserID:
		return t.ID, nil
	case core.TargetUserName, core.TargetChannelName: // `whois jaekwon`, `--from jaekwon`
		u, err := s.findUser(ctx, t.Name, exact)
		return u.ID, err
	}
	return "", usagef("사용자(@이름 또는 U…/W… ID)를 지정하라")
}

// noDMError: 해당 사용자와의 im이 없다. read는 그대로 실패하고, send만 conversations.open으로 연다.
type noDMError struct{ user, label string }

func (e noDMError) Error() string { return "@" + e.label + "와의 DM 기록이 없다" }
