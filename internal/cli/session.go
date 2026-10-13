package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	api     *slack.Client // API 호출: 요청 전체 30초
	files   *slack.Client // 파일 업로드·다운로드: 전체 제한 없음(큰 파일), Ctrl-C로 끊는다
	cache   store.Cache
	refresh bool
	app     string // xapp-(watch용, 없을 수 있다) — api에 이미 실려 있다

	users      map[string]core.User // id → user
	usersFresh bool
	chans      []core.Channel
	chansFresh bool
	groups     []slack.UserGroup // usergroups.list — 실행당 한 번(groupsErr와 함께)
	groupsErr  error
	groupsDone bool
	bots       map[string]string // bot_id → 이름(실패는 "")
}

// render는 mrkdwn을 이 세션의 이름으로 푼다.
func (s *session) render(ctx context.Context, text string) string {
	return core.Render(text, func(id string) string { return s.userName(ctx, id) }, s.channelName,
		func(id string) string { return s.groupName(ctx, id) })
}

// newAPI는 클라이언트를 만든다. timeout 0이면 요청 전체 제한이 없고(파일 전송용),
// 응답 헤더까지 30초(요청 본문을 다 보낸 뒤부터)와 Ctrl-C(context)로만 끊는다.
func newAPI(token, cookie string, timeout time.Duration, extra ...slack.Option) *slack.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 30 * time.Second
	hc := &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: checkRedirect}
	if cookie != "" {
		hc.Transport = cookieTransport{cookie: cookie, next: tr}
	}
	rc := slack.DefaultRetryConfig()
	rc.RetryAfterDuration = 5 * time.Second                                         // Retry-After가 없을 때 기본 60s 멈춤 방지
	opts := []slack.Option{slack.OptionHTTPClient(hc), slack.OptionRetryConfig(rc)} // 429만 재시도
	if apiURL != "" {
		opts = append(opts, slack.OptionAPIURL(apiURL))
	}
	return slack.New(token, append(opts, extra...)...)
}

var apiURL string // 테스트에서 가짜 서버로 바꾼다

const apiTimeout = 30 * time.Second

// cookieTransport는 xoxc 세션의 d 쿠키를 Slack 호스트로 가는 요청에만 붙인다.
type cookieTransport struct {
	cookie string
	next   http.RoundTripper
}

func (t cookieTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if isSlackURL(req.URL) {
		req = req.Clone(req.Context())
		req.Header.Set("Cookie", t.cookie)
	}
	return t.next.RoundTrip(req)
}

func isSlackURL(u *url.URL) bool {
	h := u.Hostname()
	return u.Scheme == "https" && (h == "slack.com" || strings.HasSuffix(h, ".slack.com"))
}

// checkRedirect: 리다이렉트는 https Slack(또는 테스트 API 서버)으로만 따른다. 토큰 헤더는 Go가 다른 도메인에서
// 이미 떼지만, slack-go는 첫 URL만 검사하므로 http 다운그레이드나 Slack 밖 페이지를 "파일"로 받는 것을 여기서 막는다.
// 직접 정의하면 Go 기본 10홉 제한이 사라져서 다시 둔다.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("리다이렉트가 너무 많다")
	}
	if isSlackURL(req.URL) || apiURL != "" && strings.HasPrefix(req.URL.String(), apiURL) {
		return nil
	}
	return fmt.Errorf("Slack 밖으로 리다이렉트하지 않는다: %s", req.URL.Host)
}

// sendChannel은 보낼 대화 ID다. @이름은 정확 일치만(오발송 방지), DM이 없으면 연다.
func (s *session) sendChannel(ctx context.Context, t core.Target) (string, error) {
	if t.Kind == core.TargetUserName {
		id, err := s.userID(ctx, t, true)
		if err != nil {
			return "", err
		}
		t = core.Target{Kind: core.TargetUserID, ID: id}
	}
	ch, err := s.channelID(ctx, t)
	if nd := (noDMError{}); errors.As(err, &nd) {
		c, _, _, err := s.api.OpenConversationContext(ctx, &slack.OpenConversationParameters{Users: []string{nd.user}})
		if err != nil {
			return "", err
		}
		return c.ID, nil
	}
	return ch, err
}

// open은 GSLACK_TOKEN(있으면) 또는 프로필로 세션을 연다.
func (a *app) open(ctx context.Context) (*session, error) {
	if a.profile != "" && !store.ValidProfileName(a.profile) {
		return nil, usagef("프로필 이름 %q가 올바르지 않다", a.profile)
	}
	if tok := os.Getenv("GSLACK_TOKEN"); tok != "" {
		cookie, app := cookieHeader(os.Getenv("GSLACK_COOKIE")), os.Getenv("GSLACK_APP_TOKEN")
		api := newAPI(tok, cookie, apiTimeout, slack.OptionAppLevelToken(app))
		res, err := api.AuthTestContext(ctx)
		if err != nil {
			return nil, err
		}
		return &session{name: "env", prof: profileFrom(res, tok), api: api, files: newAPI(tok, cookie, 0), app: app,
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
	return &session{name: name, prof: prof, api: newAPI(sec.Token, sec.Cookie, apiTimeout, slack.OptionAppLevelToken(sec.AppToken)),
		files: newAPI(sec.Token, sec.Cookie, 0), app: sec.AppToken, cache: store.OpenCache(name), refresh: a.refresh}, nil
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
	items, ok := store.LoadCache[core.User](s.cache, "users", store.CacheTTL)
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
	items, ok := store.LoadCache[core.Channel](s.cache, "channels", store.CacheTTL)
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

// loadGroups는 사용자 그룹을 실행당 한 번 받는다(디스크 캐시 없음 — 라벨 없는 그룹 멘션과 watch만 쓴다).
// 429·네트워크 오류도 기억한다 — 멘션마다 다시 부르는 것보다 @subteam으로 두는 게 낫다.
func (s *session) loadGroups(ctx context.Context) ([]slack.UserGroup, error) {
	if !s.groupsDone {
		s.groups, s.groupsErr = s.api.GetUserGroupsContext(ctx, slack.GetUserGroupsOptionIncludeUsers(true))
		s.groupsDone = true
	}
	return s.groups, s.groupsErr
}

func (s *session) groupName(ctx context.Context, id string) string {
	gs, _ := s.loadGroups(ctx)
	for _, g := range gs {
		if g.ID == id {
			return g.Handle
		}
	}
	return ""
}

// botName은 bot_id만 있는 메시지(username·bot_profile 없음)의 봇 이름이다. 확정 실패도 기억한다.
func (s *session) botName(ctx context.Context, id string) string {
	if n, ok := s.bots[id]; ok {
		return n
	}
	b, err := s.api.GetBotInfoContext(ctx, slack.GetBotInfoParameters{Bot: id})
	if s.bots == nil {
		s.bots = map[string]string{}
	}
	var se slack.SlackErrorResponse
	switch {
	case err == nil:
		s.bots[id] = b.Name
	case errors.As(err, &se): // bot_not_found·missing_scope — 429·네트워크는 다시 시도
		s.bots[id] = ""
	}
	return s.bots[id]
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
