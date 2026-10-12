package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
)

// fakeSlack는 읽기 API 몇 개만 흉내 낸다. 요청 폼을 기록한다.
type fakeSlack struct {
	mu        sync.Mutex
	reqs      map[string][]url.Values
	auth      string // auth.test 응답 오류 코드(비면 ok)
	limitUser bool   // users.list가 계속 429
	failInfo  bool   // users.info 실패
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	method := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	f.reqs[method] = append(f.reqs[method], r.Form)
	f.mu.Unlock()
	var body string
	switch method {
	case "auth.test":
		if f.auth != "" {
			body = `{"ok":false,"error":"` + f.auth + `"}`
		} else {
			body = `{"ok":true,"url":"https://acme.slack.com/","team":"Acme","user":"me","team_id":"T1","user_id":"U0MEMEMEME"}`
		}
	case "users.list":
		if f.limitUser {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		body = `{"ok":true,"members":[{"id":"U1AAAAAAAA","name":"jaekwon","real_name":"Jaekwon Park","profile":{"display_name":"jk"}},
			{"id":"U2TTTTTTTT","name":"minsu","real_name":"Minsu Kim","profile":{"display_name":"minsu (민수)"}},
			{"id":"U3HHHHHHHH","name":"jisu","real_name":"Jisu Lee","profile":{"display_name":"지수"}}]}`
	case "conversations.info":
		ch := r.Form.Get("channel")
		body = `{"ok":true,"channel":{"id":"` + ch + `","last_read":"1700000002.000000"}}`
		if ch == "C9GONEGONE" {
			body = `{"ok":false,"error":"channel_not_found"}`
			break
		}
		if ch == "D3UNREADDM" {
			body = `{"ok":true,"channel":{"id":"D3UNREADDM","is_im":true,"last_read":"1700000001.000000","latest":{"ts":"1700000005.000000"}}}`
			break
		}
		if ch == "G1NEVERREAD" {
			body = `{"ok":true,"channel":{"id":"` + ch + `","last_read":"0000000000.000000"}}`
		}
		if strings.HasPrefix(ch, "D") {
			body = `{"ok":true,"channel":{"id":"` + ch + `","is_im":true,"last_read":"1700000004.000000","latest":{"ts":"1700000004.000000"}}}`
		}
	case "conversations.replies":
		if ts := r.Form.Get("oldest"); ts != "" { // fetchMessage: …05는 내 메시지, 나머지는 남의 것
			m := map[string]string{"ts": ts, "user": "U1AAAAAAAA", "text": "원문"}
			switch ts {
			case "1700000005.000000": // gslack으로 보낸 내 메시지 — 앱의 bot_id도 붙는다
				m["user"], m["bot_id"] = "U0MEMEMEME", "B0APPAPPAP"
			case "1700000006.000000": // 봇
				m["user"], m["bot_id"] = "", "B1"
			case "1700000007.000000": // 내 입장 메시지
				m["user"], m["subtype"] = "U0MEMEMEME", "channel_join"
			}
			b, _ := json.Marshal(m)
			body = `{"ok":true,"messages":[` + string(b) + `]}`
			break
		}
		body = `{"ok":true,"has_more":false,"messages":[
			{"ts":"1700000002.000000","thread_ts":"1700000002.000000","reply_count":1,"user":"U1AAAAAAAA","text":"parent"},
			{"ts":"1700000002.500000","thread_ts":"1700000002.000000","user":"U1AAAAAAAA","text":"reply"}]}`
	case "conversations.open":
		body = `{"ok":true,"channel":{"id":"D2OPENEDDM"}}`
	case "conversations.mark":
		body = `{"ok":true}`
	case "chat.postMessage":
		tts := r.Form.Get("thread_ts")
		if tts == "1700000001.000000" {
			tts = "" // 부모가 지워져 최상위로 올라간 경우
		}
		m, _ := json.Marshal(map[string]string{"ts": "1700000099.000000", "user": "U0MEMEMEME",
			"text": r.Form.Get("text"), "thread_ts": tts})
		body = `{"ok":true,"channel":"` + r.Form.Get("channel") + `","ts":"1700000099.000000","message":` + string(m) + `}`
	case "users.getPresence":
		body = `{"ok":true,"presence":"active"}`
	case "users.info":
		if f.failInfo {
			body = `{"ok":false,"error":"fatal_error"}`
			break
		}
		body = `{"ok":true,"user":{"id":"` + r.Form.Get("user") + `","name":"ext","real_name":"External",
			"profile":{"status_text":"점심","status_emoji":":ramen:","status_expiration":0}}}`
	case "search.messages":
		body = `{"ok":true,"messages":{"total":2,"matches":[
			{"ts":"1700000009.000000","user":"U1AAAAAAAA","text":"dm hit","channel":{"id":"D1DMDMDMDM","name":"U1AAAAAAAA"},
			 "permalink":"https://acme.slack.com/archives/D1DMDMDMDM/p1700000009000000"},
			{"ts":"1700000008.000000","user":"U1AAAAAAAA","text":"thread hit","channel":{"id":"C1GENERAL0","name":"general"},
			 "permalink":"https://acme.slack.com/archives/C1GENERAL0/p1700000008000000?thread_ts=1700000002.000000&cid=C1GENERAL0"}]}}`
	case "reactions.add", "reactions.remove":
		switch r.Form.Get("name") {
		case "dup":
			body = `{"ok":false,"error":"already_reacted"}`
		case "gone":
			body = `{"ok":false,"error":"no_reaction"}`
		case "bad":
			body = `{"ok":false,"error":"invalid_name"}`
		default:
			body = `{"ok":true}`
		}
	case "chat.update", "chat.delete", "users.profile.set":
		body = `{"ok":true,"channel":"` + r.Form.Get("channel") + `","ts":"` + r.Form.Get("ts") + `"}`
	case "users.conversations":
		body = `{"ok":true,"channels":[{"id":"C1GENERAL0","name":"general","is_channel":true},
			{"id":"D1DMDMDMDM","is_im":true,"user":"U1AAAAAAAA"},
			{"id":"G1NEVERREAD","name":"never","is_private":true},
			{"id":"D3UNREADDM","is_im":true,"user":"U3HHHHHHHH"},
			{"id":"C9GONEGONE","name":"gone","is_channel":true}],"response_metadata":{"next_cursor":""}}`
	case "conversations.history":
		if r.Form.Get("channel") == "D3UNREADDM" { // 본인 메시지와 입장은 세지 않는다
			body = `{"ok":true,"messages":[{"ts":"1700000005.000000","user":"U0MEMEMEME","text":"mine"},
				{"ts":"1700000004.000000","user":"U3HHHHHHHH","subtype":"channel_join","text":"joined"},
				{"ts":"1700000003.000000","user":"U3HHHHHHHH","text":"안 읽은 DM"}]}`
			break
		}
		if r.Form.Get("oldest") == "0000000000.000000" {
			body = `{"ok":false,"error":"invalid_ts_oldest"}`
			break
		}
		if r.Form.Get("cursor") == "" { // 최신순
			body = `{"ok":true,"has_more":true,"response_metadata":{"next_cursor":"c2"},"messages":[
				{"ts":"1700000004.000000","user":"U1AAAAAAAA","text":"four &amp; <@U1AAAAAAAA>"},
				{"ts":"1700000003.000000","bot_id":"B1","username":"hook","bot_profile":{"name":"Deploy Bot"},"text":"",
				 "blocks":[{"type":"section","text":{"type":"mrkdwn","text":"deployed *v1*"}}]}]}`
		} else {
			body = `{"ok":true,"has_more":false,"messages":[
				{"ts":"1700000002.000000","user":"U9EXTERNAL","text":"two","reply_count":2,"thread_ts":"1700000002.000000"},
				{"ts":"1700000001.000000","user":"U1AAAAAAAA","text":"one","edited":{"user":"U1AAAAAAAA","ts":"1"}}]}`
		}
	default:
		body = `{"ok":false,"error":"unknown_method"}`
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(body))
}

func setup(t *testing.T) *fakeSlack {
	f := &fakeSlack{reqs: map[string][]url.Values{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	apiURL = srv.URL + "/"
	t.Cleanup(func() { apiURL = "" })
	t.Setenv("GSLACK_TOKEN", "xoxp-test")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return f
}

func gslack(args ...string) (code int, stdout, stderr string) {
	return gslackIn("", args...)
}

func gslackIn(stdin string, args ...string) (code int, stdout, stderr string) {
	var o, e bytes.Buffer
	code = Execute(context.Background(), args, strings.NewReader(stdin), &o, &e)
	return code, o.String(), e.String()
}

func TestReadJSON(t *testing.T) {
	f := setup(t)
	code, out, errOut := gslack("read", "#general", "-n", "4", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var msgs []core.Message
	if err := json.Unmarshal([]byte(out), &msgs); err != nil {
		t.Fatal(err, out)
	}
	if len(msgs) != 4 || msgs[0].TS != "1700000001.000000" || msgs[3].TS != "1700000004.000000" {
		t.Fatalf("순서/개수 오류: %+v", msgs)
	}
	for _, m := range msgs {
		if m.Channel != "C1GENERAL0" || !strings.HasPrefix(m.Permalink, "https://acme.slack.com/archives/C1GENERAL0/p") {
			t.Errorf("channel/permalink 주입 안 됨: %+v", m)
		}
	}
	if !msgs[0].Edited || msgs[0].UserName != "jk" {
		t.Errorf("edited/user_name: %+v", msgs[0])
	}
	if msgs[1].UserName != "External" || msgs[1].ReplyCount != 2 || msgs[1].ThreadTS == "" {
		t.Errorf("users.info 폴백/스레드 부모: %+v", msgs[1])
	}
	if m := msgs[2]; !m.Bot || m.User != "B1" || m.UserName != "Deploy Bot" || m.Text != "deployed *v1*" {
		t.Errorf("봇 blocks-only: %+v", m)
	}
	if h := f.reqs["conversations.history"]; len(h) != 2 || h[0].Get("limit") != "4" || h[1].Get("cursor") != "c2" {
		t.Errorf("페이지네이션 요청: %v", h)
	}
}

func TestReadHumanAndCache(t *testing.T) {
	f := setup(t)
	code, out, errOut := gslack("read", "general", "-n", "2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"── ", "1700000003.000000", "@Deploy Bot: deployed *v1*", "@jk: four & @jk"} {
		if !strings.Contains(out, want) {
			t.Errorf("출력에 %q 없음:\n%s", want, out)
		}
	}
	gslack("read", "@jaekwon", "-n", "1") // @user → im 매칭, 캐시 재사용
	if n := len(f.reqs["users.conversations"]); n != 1 {
		t.Errorf("users.conversations %d회 — 캐시 미사용", n)
	}
	if h := f.reqs["conversations.history"]; h[len(h)-1].Get("channel") != "D1DMDMDMDM" {
		t.Errorf("@jaekwon DM 해석: %v", h[len(h)-1])
	}
}

func TestReadPermalink(t *testing.T) {
	f := setup(t)
	if code, _, e := gslack("read", "https://acme.slack.com/archives/C1GENERAL0/p1700000004000000", "-n", "1", "--json"); code != 0 {
		t.Fatal(e)
	}
	h := f.reqs["conversations.history"][0]
	if h.Get("latest") != "1700000004.000000" || h.Get("inclusive") != "1" || h.Get("channel") != "C1GENERAL0" {
		t.Errorf("permalink 파라미터: %v", h)
	}
	code, _, e := gslack("read", "https://acme.slack.com/archives/C1GENERAL0/p1700000009000000?thread_ts=1700000002.000000")
	if code != 2 || !strings.Contains(e, "thread") {
		t.Errorf("스레드 답글 링크: exit %d %s", code, e)
	}
}

func TestExitCodes(t *testing.T) {
	f := setup(t)
	cases := []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"read", "#general", "--json", "--raw"}, 2, "usage"},
		{[]string{"read", "#nope", "--json"}, 2, "usage"},
		{[]string{"read", "--json"}, 2, "usage"},      // 인자 개수
		{[]string{"nosuchcmd", "--json"}, 2, "usage"}, // 알 수 없는 명령
		{[]string{"read", "#general", "--bogus", "--json"}, 2, "usage"},
		{[]string{"read", "#general", "--since", "x", "--json"}, 2, "usage"},
	}
	for _, c := range cases {
		code, _, e := gslack(c.args...)
		var j map[string]string
		json.Unmarshal([]byte(e), &j)
		if code != c.code || j["error"] != c.err {
			t.Errorf("%v: exit %d stderr %s", c.args, code, e)
		}
	}
	f.auth = "invalid_auth"
	code, _, e := gslack("auth", "status", "--json")
	var j map[string]string
	json.Unmarshal([]byte(e), &j)
	if code != 1 || j["error"] != "invalid_auth" || !strings.Contains(j["message"], "auth login") {
		t.Errorf("invalid_auth: exit %d %s", code, e)
	}
}

func TestLoginWithFileSecrets(t *testing.T) {
	setup(t)
	t.Setenv("GSLACK_SECRET_BACKEND", "file")
	t.Setenv("GSLACK_TOKEN", "") // stdin으로 받는다
	var o, e bytes.Buffer
	code := Execute(context.Background(), []string{"auth", "login"}, strings.NewReader("xoxp-piped\n\n"), &o, &e)
	if code != 0 || !strings.Contains(o.String(), "acme") {
		t.Fatalf("login exit %d: %s %s", code, o.String(), e.String())
	}
	code, out, _ := gslack("auth", "list")
	if code != 0 || !strings.Contains(out, "* acme") {
		t.Errorf("list: %s", out)
	}
	if code, _, e := gslack("auth", "status"); code != 0 {
		t.Errorf("status via profile: %s", e)
	}
	if code, _, _ := gslack("auth", "logout"); code != 0 {
		t.Error("logout")
	}
	if code, _, e := gslack("channels", "--json"); code != 1 || !strings.Contains(e, "not_logged_in") {
		t.Errorf("after logout: %d %s", code, e)
	}
}

func TestCookieTransport(t *testing.T) {
	var got []string
	next := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.URL.Host+"="+r.Header.Get("Cookie"))
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	rt := cookieTransport{cookie: "d=x", next: next}
	for _, u := range []string{"https://slack.com/api/x", "https://files.slack.com/f", "https://evilslack.com/",
		"https://slack.com.evil.com/", "http://slack.com/"} {
		req, _ := http.NewRequest("GET", u, nil)
		rt.RoundTrip(req)
		if req.Header.Get("Cookie") != "" {
			t.Error("원본 요청이 바뀌었다")
		}
	}
	want := []string{"slack.com=d=x", "files.slack.com=d=x", "evilslack.com=", "slack.com.evil.com=", "slack.com="}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestParseSince(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for in, want := range map[string]string{
		"2h": "1699992800.000000", "3d": "1699740800.000000", "2023-11-14T22:13:20Z": "1700000000.000000",
	} {
		if got, err := parseSince(in, now); err != nil || got != want {
			t.Errorf("parseSince(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"x", "-2h", "2w", "-1d"} {
		if _, err := parseSince(in, now); err == nil {
			t.Errorf("parseSince(%q) accepted", in)
		}
	}
}

func TestReadParamsTrimAndRaw(t *testing.T) {
	f := setup(t)
	code, out, e := gslack("read", "C1GENERAL0", "-n", "3", "--since", "2023-11-14T22:13:20Z", "--before", "1700000009.000000", "--json")
	if code != 0 {
		t.Fatal(e)
	}
	var msgs []core.Message
	json.Unmarshal([]byte(out), &msgs)
	if len(msgs) != 3 || msgs[0].TS != "1700000002.000000" { // 가장 오래된 1개가 잘려야 한다
		t.Errorf("최신 n개 트림: %+v", msgs)
	}
	if h := f.reqs["conversations.history"][0]; h.Get("oldest") != "1700000000.000000" || h.Get("latest") != "1700000009.000000" || h.Get("inclusive") == "1" {
		t.Errorf("since/before 파라미터: %v", h)
	}
	_, out, _ = gslack("read", "C1GENERAL0", "-n", "2", "--raw")
	var raw []map[string]any
	json.Unmarshal([]byte(out), &raw)
	if len(raw) != 2 || raw[0]["ts"] != "1700000004.000000" {
		t.Errorf("--raw는 Slack 원본 순서(최신순): %v", raw)
	}
}

func TestUsageGuards(t *testing.T) {
	setup(t)
	for _, args := range [][]string{
		{"channels", "--raw"},
		{"auth", "status", "--raw"},
		{"read", "https://acme.slack.com/archives/C1GENERAL0/p1700000004000000", "--before", "1700000001.000000"},
		{"read", "https://acme.slack.com/archives/C1GENERAL0/p1234567"},
		{"channels", "--profile", "../x"},
		{"auth", "logout", "../x"},
	} {
		if code, _, e := gslack(args...); code != 2 {
			t.Errorf("%v: exit %d %s", args, code, e)
		}
	}
	if code, _, e := gslack("channels", "--bogus", "--json=true"); code != 2 || !strings.HasPrefix(e, "{") {
		t.Errorf("--json=true 오류 형식: %s", e)
	}
}

func TestUsersRateLimitStops(t *testing.T) {
	f := setup(t)
	f.limitUser = true
	done := make(chan struct{})
	var code int
	var e string
	go func() { code, _, e = gslack("users", "--json"); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("users.list 429에서 멈추지 않는다")
	}
	if code != 1 || !strings.Contains(e, "rate_limited") {
		t.Errorf("exit %d %s", code, e)
	}
}

func TestProfileName(t *testing.T) {
	setup(t)
	t.Setenv("GSLACK_SECRET_BACKEND", "file")
	t.Setenv("GSLACK_TOKEN", "")
	var o, e bytes.Buffer
	if code := Execute(context.Background(), []string{"auth", "login", "--profile", ".."}, strings.NewReader("xoxp-x\n\n"), &o, &e); code != 2 {
		t.Errorf("--profile .. 허용됨: %d %s", code, e.String())
	}
}

func TestWhoisPartialName(t *testing.T) {
	setup(t)
	for in, want := range map[string]string{"@jaekwon": "U1AAAAAAAA", "@민수": "U2TTTTTTTT", "@MINSU": "U2TTTTTTTT", "@lee": "U3HHHHHHHH"} {
		if code, out, errOut := gslack("whois", in, "--json"); code != 0 || !strings.Contains(out, want) {
			t.Errorf("whois %s = %d %s %s; want %s", in, code, out, errOut, want)
		}
	}
	if code, _, errOut := gslack("whois", "@su"); code != 2 || !strings.Contains(errOut, "여러 명") {
		t.Errorf("@su 모호: %d %s", code, errOut)
	}
	if code, _, _ := gslack("whois", "@nobody"); code != 2 {
		t.Errorf("@nobody: %d", code)
	}
}

func TestThread(t *testing.T) {
	f := setup(t)
	code, out, errOut := gslack("thread", "https://acme.slack.com/archives/C1GENERAL0/p1700000002500000?thread_ts=1700000002.000000&cid=C1GENERAL0")
	if code != 0 || !strings.Contains(out, "parent") || strings.Index(out, "parent") > strings.Index(out, "reply") {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	if r := f.reqs["conversations.replies"][0]; r.Get("ts") != "1700000002.000000" || r.Get("channel") != "C1GENERAL0" || r.Get("limit") != "200" {
		t.Errorf("replies 파라미터: %v", r)
	}
	if code, _, _ := gslack("thread", "general", "1700000002.000000", "--json"); code != 0 {
		t.Errorf("target ts: %d", code)
	}
	for _, args := range [][]string{{"thread", "general"}, {"thread", "general", "x"}} {
		if code, _, _ := gslack(args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
}

func TestSend(t *testing.T) {
	f := setup(t)
	post := func() url.Values { p := f.reqs["chat.postMessage"]; return p[len(p)-1] }
	code, out, errOut := gslack("send", "#general", "hi *there*")
	if code != 0 || !strings.Contains(out, "https://acme.slack.com/archives/C1GENERAL0/p1700000099000000") {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	if p := post(); p.Get("channel") != "C1GENERAL0" || p.Get("text") != "hi *there*" || p.Has("thread_ts") {
		t.Errorf("post: %v", p)
	}
	if code, _, errOut := gslackIn("  from stdin\n", "send", "general", "-"); code != 0 || post().Get("text") != "from stdin" {
		t.Errorf("stdin: %d %s %v", code, errOut, post())
	}
	gslack("send", "https://acme.slack.com/archives/C1GENERAL0/p1700000002500000?thread_ts=1700000002.000000&cid=C1GENERAL0", "re")
	if p := post(); p.Get("thread_ts") != "1700000002.000000" || p.Get("channel") != "C1GENERAL0" {
		t.Errorf("permalink → 스레드 답글: %v", p)
	}
	code, out, _ = gslack("send", "@minsu", "hello", "--json") // im 없음 → open
	if o := f.reqs["conversations.open"]; code != 0 || len(o) != 1 || o[0].Get("users") != "U2TTTTTTTT" || post().Get("channel") != "D2OPENEDDM" {
		t.Errorf("DM open: %d %v %v", code, o, post())
	}
	var m core.Message
	if json.Unmarshal([]byte(out), &m); m.Channel != "D2OPENEDDM" || m.TS != "1700000099.000000" {
		t.Errorf("--json: %s", out)
	}
	if code, _, _ := gslack("send", "@jk", "hi"); code != 0 || post().Get("channel") != "D1DMDMDMDM" { // 표시명 정확 일치
		t.Errorf("@jk: %d %v", code, post())
	}
	code, out, _ = gslack("send", "general", "re", "--thread", "1700000001.000000", "--json")
	if json.Unmarshal([]byte(out), &m); code != 0 || post().Get("thread_ts") != "1700000001.000000" || m.ThreadTS != "" {
		t.Errorf("--thread: 출력은 응답의 thread_ts를 따른다: %d %v %+v", code, post(), m)
	}
	n := len(f.reqs["chat.postMessage"])
	for _, args := range [][]string{
		{"send", "general", "  "},                  // 빈 본문
		{"send", "@민수", "hi"},                      // 부분 일치는 거절
		{"send", "general", "hi", "--thread", "x"}, // ts 아님
		{"send", "general", "a", "b"},              // 본문은 인자 하나
	} {
		if code, _, _ := gslack(args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
	if len(f.reqs["chat.postMessage"]) != n {
		t.Error("거절된 send가 전송됐다")
	}
	if code, _, e := gslack("read", "@minsu"); code != 2 || len(f.reqs["conversations.open"]) != 1 || !strings.Contains(e, "@minsu와의 DM") {
		t.Errorf("read는 DM을 열지 않는다: %d", code)
	}
}

func TestMark(t *testing.T) {
	f := setup(t)
	if code, _, _ := gslack("mark", "general"); code != 2 || len(f.reqs) != 0 {
		t.Errorf("비TTY + --yes 없음: %d %v", code, f.reqs)
	}
	marked := func() string { m := f.reqs["conversations.mark"]; return m[len(m)-1].Get("ts") }
	if code, _, errOut := gslack("mark", "general", "--yes"); code != 0 || marked() != "1700000004.000000" {
		t.Errorf("mark 최신: %d %s", code, errOut)
	}
	if code, _, _ := gslack("mark", "https://acme.slack.com/archives/C1GENERAL0/p1700000002500000?thread_ts=1700000002.000000&cid=C1GENERAL0", "--yes"); code != 2 {
		t.Errorf("답글 링크 mark: %d", code)
	}
	// last_read(…02)보다 과거로는 되돌리지 않는다
	if code, _, _ := gslack("mark", "https://acme.slack.com/archives/C1GENERAL0/p1700000001000000", "--yes"); code != 2 || len(f.reqs["conversations.mark"]) != 1 {
		t.Errorf("과거로 mark: %d", code)
	}
	for _, args := range [][]string{{"read", "general", "-n", "1", "--mark"}, {"read", "general", "-n", "1", "--mark", "--raw"}} {
		f.reqs["conversations.mark"] = nil
		if code, _, _ := gslack(args...); code != 0 || marked() != "1700000004.000000" {
			t.Errorf("%v: %d", args, code)
		}
	}
	if code, _, _ := gslack("read", "general", "--mark", "--before", "1700000003.000000"); code != 2 {
		t.Errorf("--mark --before: %d", code)
	}
}

func TestUnread(t *testing.T) {
	f := setup(t)
	code, out, errOut := gslack("unread", "--all", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var rows []struct {
		Channel core.Channel `json:"channel"`
		Count   int          `json:"count"`
		More    bool         `json:"more"`
		Latest  core.Message `json:"latest"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err, out)
	}
	// general: last_read 이후 첫 페이지 2건(has_more). never: last_read 0 → oldest 없이. DM: latest == last_read라 history 생략
	got := map[string]string{}
	for _, r := range rows {
		got[r.Channel.ID] = fmt.Sprintf("%d %t %s", r.Count, r.More, r.Latest.TS)
	}
	// D3: 본인·입장 제외 1건. C9: channel_not_found는 건너뜀
	if want := "2 true 1700000004.000000"; len(rows) != 3 || got["C1GENERAL0"] != want || got["G1NEVERREAD"] != want || got["D3UNREADDM"] != "1 false 1700000003.000000" {
		t.Errorf("rows: %v", got)
	}
	for _, h := range f.reqs["conversations.history"] {
		if h.Get("channel") == "D1DMDMDMDM" {
			t.Error("읽은 DM에 history 호출")
		}
		if h.Get("channel") == "C1GENERAL0" && h.Get("oldest") != "1700000002.000000" {
			t.Errorf("oldest: %v", h)
		}
	}
	f.reqs["conversations.info"] = nil
	if code, out, _ := gslack("unread"); code != 0 || !strings.Contains(out, "안 읽은 DM") || len(f.reqs["conversations.info"]) != 2 {
		t.Errorf("기본은 im·mpim만: %d %q %v", code, out, f.reqs["conversations.info"])
	}
	if code, _, _ := gslack("unread", "--all", "--type", "im"); code != 2 {
		t.Errorf("--all --type: %d", code)
	}
}

func TestWaitRateLimit(t *testing.T) {
	n := 0
	v, err := waitRateLimit(context.Background(), func() (int, error) {
		if n++; n < 3 {
			return 0, &slack.RateLimitedError{}
		}
		return 7, nil
	})
	if v != 7 || err != nil || n != 3 {
		t.Errorf("%d %v %d", v, err, n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitRateLimit(ctx, func() (int, error) { return 0, &slack.RateLimitedError{RetryAfter: time.Hour} }); err != context.Canceled {
		t.Errorf("취소: %v", err)
	}
}

func TestConfirm(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "YES\n": true, "n\n": false, "": false, "yy\n": false} {
		a := &app{in: strings.NewReader(in), errw: io.Discard}
		if got := a.confirm("? "); got != want {
			t.Errorf("confirm(%q) = %v", in, got)
		}
	}
}

func TestSearch(t *testing.T) {
	f := setup(t)
	code, out, errOut := gslack("search", "foo", "--in", "#general", "--from", "@jaekwon", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	q := f.reqs["search.messages"][0]
	if q.Get("query") != "foo in:<#C1GENERAL0> from:<@U1AAAAAAAA>" || q.Get("sort") != "timestamp" || q.Get("count") != "" {
		t.Errorf("query: %v", q) // count 20은 기본값이라 안 보낸다
	}
	var msgs []core.Message
	json.Unmarshal([]byte(out), &msgs)
	if len(msgs) != 2 || msgs[0].TS != "1700000008.000000" || msgs[0].ThreadTS != "1700000002.000000" || msgs[1].Channel != "D1DMDMDMDM" || msgs[0].UserName != "jk" {
		t.Errorf("오래된 것부터, thread_ts는 permalink에서: %+v", msgs)
	}
	code, out, _ = gslack("search", "foo", "--in", "@jaekwon", "-n", "5")
	if q := f.reqs["search.messages"][1]; code != 0 || q.Get("query") != "foo in:<@U1AAAAAAAA>" || q.Get("count") != "5" {
		t.Errorf("--in @user: %d %v", code, q)
	}
	if !strings.Contains(out, "#general  @jk: thread hit") || !strings.Contains(out, "@jk  @jk: dm hit") {
		t.Errorf("사람용:\n%s", out)
	}
	if code, _, _ := gslack("search", "foo", "-n", "101"); code != 2 {
		t.Errorf("-n 101: %d", code)
	}
}

func TestReact(t *testing.T) {
	f := setup(t)
	if code, _, e := gslack("react", "general", "1700000004.000000", ":thumbsup:"); code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	if r := f.reqs["reactions.add"][0]; r.Get("name") != "thumbsup" || r.Get("channel") != "C1GENERAL0" || r.Get("timestamp") != "1700000004.000000" {
		t.Errorf("reactions.add: %v", r)
	}
	link := "https://acme.slack.com/archives/C1GENERAL0/p1700000004000000"
	for _, args := range [][]string{{"react", link, "dup"}, {"react", link, "gone", "--remove"}} {
		if code, out, _ := gslack(args...); code != 0 || !strings.Contains(out, "이미") {
			t.Errorf("%v 멱등: %d %s", args, code, out)
		}
	}
	if len(f.reqs["reactions.remove"]) != 1 {
		t.Error("--remove가 reactions.remove를 안 불렀다")
	}
	for _, args := range [][]string{{"react", link, "bad"}, {"react", "general", "1700000004.000000"}, {"react", link, "1700000004.000000", "x"}} {
		if code, _, _ := gslack(args...); code == 0 {
			t.Errorf("%v: 성공하면 안 된다", args)
		}
	}
}

func TestEditDelete(t *testing.T) {
	f := setup(t)
	for _, args := range [][]string{{"edit", "general", "1700000005.000000", "new"}, {"delete", "general", "1700000005.000000"},
		{"edit", "general", "1700000005.000000", "-", "--yes"}, {"delete", "general", "1700000005.000000", "x", "--yes"}} {
		if code, _, _ := gslack(args...); code != 2 || len(f.reqs) != 0 {
			t.Errorf("%v: %d, 요청 %v", args, code, f.reqs) // 확인 불가·잘못된 인자는 Slack 호출 0회
		}
	}
	if code, _, e := gslack("edit", "general", "1700000005.000000", " new ", "--yes"); code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	if u := f.reqs["chat.update"][0]; u.Get("ts") != "1700000005.000000" || u.Get("text") != "new" || u.Get("channel") != "C1GENERAL0" || u.Get("blocks") != "[]" {
		t.Errorf("chat.update: %v", u)
	}
	// 남의 메시지(…04), 봇(…06), 내 입장 메시지(…07)는 거절
	for _, args := range [][]string{{"edit", "general", "1700000004.000000", "x", "--yes"}, {"delete", "general", "1700000004.000000", "--yes"},
		{"delete", "general", "1700000006.000000", "--yes"}, {"delete", "general", "1700000007.000000", "--yes"}} {
		if code, _, _ := gslack(args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
	if len(f.reqs["chat.update"]) != 1 || len(f.reqs["chat.delete"]) != 0 {
		t.Error("남의 메시지를 고치거나 지웠다")
	}
	reply := "https://acme.slack.com/archives/C1GENERAL0/p1700000005000000?thread_ts=1700000002.000000&cid=C1GENERAL0"
	if code, _, e := gslack("delete", reply, "--yes", "--json"); code != 0 || f.reqs["chat.delete"][0].Get("ts") != "1700000005.000000" {
		t.Errorf("답글 삭제: %d %s", code, e)
	}
	if r := f.reqs["conversations.replies"]; r[len(r)-1].Encode() != "channel=C1GENERAL0&include_all_metadata=0&inclusive=1&latest=1700000005.000000&limit=1&oldest=1700000005.000000&token=xoxp-test&ts=1700000005.000000" {
		t.Errorf("원문 조회는 답글 ts로: %v", r)
	}
}

func TestStatus(t *testing.T) {
	f := setup(t)
	code, out, e := gslack("status", "set", "점심", "--emoji", "ramen", "--until", "2h")
	if code != 0 || !strings.Contains(out, ":ramen: 점심") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	var p struct {
		Text  string `json:"status_text"`
		Emoji string `json:"status_emoji"`
		Exp   int64  `json:"status_expiration"`
	}
	json.Unmarshal([]byte(f.reqs["users.profile.set"][0].Get("profile")), &p)
	if want := time.Now().Add(2 * time.Hour).Unix(); p.Text != "점심" || p.Emoji != ":ramen:" || p.Exp < want-5 || p.Exp > want+5 {
		t.Errorf("profile: %+v", p)
	}
	if code, _, _ := gslack("status", "clear"); code != 0 || f.reqs["users.profile.set"][1].Get("profile") != `{"status_text":"","status_emoji":"","status_expiration":0}` {
		t.Errorf("clear: %d %v", code, f.reqs["users.profile.set"][1])
	}
	f.failInfo = true // 바꾸기는 성공, 다시 조회만 실패 → 성공(exit 0) + 경고
	if code, _, e := gslack("status", "set", "x"); code != 0 || !strings.Contains(e, "다시 조회하지 못했다") {
		t.Errorf("재조회 실패: %d %s", code, e)
	}
	if code, _, _ := gslack("status", "set", " "); code != 2 {
		t.Errorf("빈 문구: %d", code)
	}
	for _, until := range []string{"-1h", "2020-01-01T00:00:00Z", "soon"} {
		if code, _, _ := gslack("status", "set", "x", "--until", until); code != 2 {
			t.Errorf("--until %s: %d", until, code)
		}
	}
}

func TestConfirmPrompt(t *testing.T) {
	f := setup(t)
	isTTY = func(any) bool { return true }
	t.Cleanup(func() { isTTY = func(any) bool { return false } })
	if code, _, e := gslackIn("n\n", "delete", "general", "1700000005.000000"); code != 0 || !strings.Contains(e, "원문") || len(f.reqs["chat.delete"]) != 0 {
		t.Errorf("취소: %d %s %d", code, e, len(f.reqs["chat.delete"])) // 원문을 보여주고, n이면 지우지 않는다
	}
	if code, _, _ := gslackIn("y\n", "delete", "general", "1700000005.000000"); code != 0 || len(f.reqs["chat.delete"]) != 1 {
		t.Errorf("승낙: %d", code)
	}
}
