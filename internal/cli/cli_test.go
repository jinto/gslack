package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jinto/gslack/internal/core"
	"github.com/jinto/gslack/internal/store"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// fakeSlack는 읽기 API 몇 개만 흉내 낸다. 요청 폼을 기록한다.
type fakeSlack struct {
	mu        sync.Mutex
	reqs      map[string][]url.Values
	auth      string // auth.test 응답 오류 코드(비면 ok)
	limitUser bool   // users.list가 계속 429
	failInfo  bool   // users.info 실패
	upload    struct{ name, auth, data string }
	evil      string   // 다운로드 리다이렉트가 향할 외부 서버
	counts    string   // client.counts 응답("500"이면 HTTP 500)
	rtm       []string // /rtm 웹소켓으로 hello 뒤에 보낼 이벤트
	rtmAuth   string   // rtm.connect 오류 코드
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/upload/"): // files.getUploadURLExternal이 준 업로드 URL
		file, hdr, err := r.FormFile("file")
		if err == nil {
			b, _ := io.ReadAll(file)
			f.mu.Lock()
			f.upload = struct{ name, auth, data string }{hdr.Filename, r.Header.Get("Authorization"), string(b)}
			f.mu.Unlock()
		}
		w.Write([]byte("OK"))
		return
	case r.URL.Path == "/dl/partial": // 본문 중간에 끊긴다
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("0123456789"))
		w.(http.Flusher).Flush()
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
		return
	case r.URL.Path == "/rtm": // slack-go가 Origin: api.slack.com을 보내므로 Origin 검사를 끈다
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for _, m := range append([]string{`{"type":"hello"}`}, f.rtm...) {
			c.WriteMessage(websocket.TextMessage, []byte(m))
		}
		for { // 닫으면 slack-go가 다시 붙어 재전송한다 — 클라이언트가 끊을 때까지 둔다
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		w.Write([]byte("DATA-" + strings.TrimPrefix(r.URL.Path, "/dl/")))
		return
	case r.URL.Path == "/redirect":
		http.Redirect(w, r, f.evil, http.StatusFound)
		return
	}
	r.ParseForm()
	method := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	f.reqs[method] = append(f.reqs[method], r.Form)
	f.mu.Unlock()
	var body string
	switch method {
	case "rtm.connect":
		if f.rtmAuth != "" {
			body = `{"ok":false,"error":"` + f.rtmAuth + `"}`
		} else {
			body = `{"ok":true,"url":"ws://` + r.Host + `/rtm","self":{"id":"U0MEMEMEME"}}`
		}
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
			switch ts {
			case "1700000010.000000": // 파일 두 개(하나는 외부)
				b = []byte(`{"ts":"1700000010.000000","user":"U1AAAAAAAA","files":[{"id":"F1FILEFILE"},{"id":"F2EXTERNAL"}]}`)
			case "1700000011.000000": // 하나는 받고 하나는 실패
				b = []byte(`{"ts":"1700000011.000000","user":"U1AAAAAAAA","files":[{"id":"F5PARTIAL0"},{"id":"F1FILEFILE"}]}`)
			}
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
	case "client.counts":
		if f.counts == "500" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body = f.counts
	case "files.getUploadURLExternal":
		body = `{"ok":true,"upload_url":"http://` + r.Host + `/upload/F1","file_id":"F1FILEFILE"}`
	case "files.completeUploadExternal":
		body = `{"ok":true,"files":[{"id":"F1FILEFILE","title":"report.txt"}]}`
	case "files.info":
		dl := "http://" + r.Host + "/dl/"
		body = map[string]string{
			"F1FILEFILE": `{"ok":true,"file":{"id":"F1FILEFILE","name":"report.pdf","url_private_download":"` + dl + `report"}}`,
			"F2EXTERNAL": `{"ok":true,"file":{"id":"F2EXTERNAL","name":"doc","is_external":true,"permalink":"https://docs.google.com/x"}}`,
			"F3EVILNAME": `{"ok":true,"file":{"id":"F3EVILNAME","name":"../../evil.txt","url_private_download":"` + dl + `evil"}}`,
			"F4REDIRECT": `{"ok":true,"file":{"id":"F4REDIRECT","name":"r.bin","url_private_download":"http://` + r.Host + `/redirect"}}`,
			"F5PARTIAL0": `{"ok":true,"file":{"id":"F5PARTIAL0","name":"p.bin","url_private_download":"` + dl + `partial"}}`,
		}[r.Form.Get("file")]
	case "chat.scheduleMessage":
		body = `{"ok":true,"channel":"` + r.Form.Get("channel") + `","scheduled_message_id":"Q1SCHEDULE","post_at":` + r.Form.Get("post_at") + `}`
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
	case "bots.info":
		body = `{"ok":false,"error":"bot_not_found"}`
		if r.Form.Get("bot") == "B2BOTBOTBO" {
			body = `{"ok":true,"bot":{"id":"B2BOTBOTBO","name":"jenkins"}}`
		}
	case "usergroups.list":
		body = `{"ok":true,"usergroups":[{"id":"S1DEVSDEVS","handle":"devs","users":["U0MEMEMEME"]}]}`
	case "conversations.history":
		if r.Form.Get("channel") == "C2RENDER00" { // 렌더링 결함 G1~G3(2026-10-13 실측)
			body = `{"ok":true,"messages":[
				{"ts":"1700000006.000000","bot_id":"B4PROFILED","bot_profile":{"name":"Deploy Bot"},"text":"x"},
				{"ts":"1700000005.000000","bot_id":"B3GONEGONE","text":"again"},
				{"ts":"1700000004.000000","bot_id":"B3GONEGONE","text":"gone"},
				{"ts":"1700000003.000000","bot_id":"B2BOTBOTBO","text":"<!subteam^S1DEVSDEVS> <!subteam^S9NOPENOPE> build"},
				{"ts":"1700000002.000000","user":"U1AAAAAAAA","text":"","blocks":[{"type":"rich_text","elements":[{"type":"rich_text_section",
				 "elements":[{"type":"usergroup","usergroup_id":"S1DEVSDEVS"},{"type":"text","text":" "},{"type":"broadcast","range":"here"}]}]}]},
				{"ts":"1700000001.000000","user":"U1AAAAAAAA","text":"\n\n    go test\n  \n"}]}`
			break
		}
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

func TestUnreadClientCounts(t *testing.T) {
	f := setup(t)
	t.Setenv("GSLACK_TOKEN", "xoxc-test") // session
	f.counts = `{"ok":true,"channels":[{"id":"C1GENERAL0","has_unreads":true},{"id":"G1NEVERREAD","has_unreads":false}],
		"ims":[{"id":"D3UNREADDM","has_unreads":true}],"mpims":[]}`
	code, out, errOut := gslack("unread", "--all", "--json")
	var info []string
	for _, r := range f.reqs["conversations.info"] {
		info = append(info, r.Get("channel"))
	}
	slices.Sort(info)
	if code != 0 || errOut != "" || strings.Join(info, ",") != "C1GENERAL0,D3UNREADDM" || !strings.Contains(out, "D3UNREADDM") {
		t.Errorf("counts: %d %q info=%v", code, errOut, info)
	}
	if c := f.reqs["client.counts"]; len(c) != 1 || c[0].Get("token") != "xoxc-test" {
		t.Errorf("client.counts 요청: %v", c)
	}
	// 실패하면 경고하고 전부 훑는다
	for _, bad := range []string{`{"ok":false,"error":"unknown_method"}`, "500", "{"} {
		f.counts, f.reqs["conversations.info"] = bad, nil
		code, _, errOut := gslack("unread", "--all", "--json")
		if code != 0 || !strings.Contains(errOut, "client.counts 실패") || len(f.reqs["conversations.info"]) < 4 {
			t.Errorf("%s 폴백: %d %q %d", bad, code, errOut, len(f.reqs["conversations.info"]))
		}
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

func TestUpload(t *testing.T) {
	f := setup(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	os.WriteFile(path, []byte("hello"), 0o600)
	code, out, e := gslack("upload", "#general", path, "--thread", "1700000002.000000", "--comment", "hi")
	if code != 0 || !strings.Contains(out, "올렸다 report.txt (F1FILEFILE)") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	if u := f.upload; u.name != "report.txt" || u.data != "hello" || u.auth != "Bearer xoxp-test" {
		t.Errorf("업로드 본문: %+v", u)
	}
	if g := f.reqs["files.getUploadURLExternal"][0]; g.Get("filename") != "report.txt" || g.Get("length") != "5" {
		t.Errorf("getUploadURL: %v", g)
	}
	if c := f.reqs["files.completeUploadExternal"][0]; c.Get("channel_id") != "C1GENERAL0" || c.Get("thread_ts") != "1700000002.000000" || c.Get("initial_comment") != "hi" {
		t.Errorf("complete: %v", c)
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, nil, 0o600)
	f.reqs = map[string][]url.Values{}
	for _, args := range [][]string{{"upload", "general", empty}, {"upload", "general", dir}, {"upload", "general", filepath.Join(dir, "none")},
		{"upload", "general", path, "--thread", "x"}} {
		if code, _, _ := gslack(args...); code != 2 || len(f.reqs) != 0 {
			t.Errorf("%v: %d %v", args, code, f.reqs) // Slack 호출 전에 거절
		}
	}
	if code, _, _ := gslack("upload", "@민수", path); code != 2 || len(f.reqs["files.getUploadURLExternal"]) != 0 {
		t.Errorf("부분 일치 이름으로 올리면 안 된다: %d", code)
	}
	if code, _, _ := gslack("upload", "general", "-"); code != 2 {
		t.Errorf("stdin: %d", code)
	}
	gslack("upload", "@minsu", path, "--title", "T") // DM 없음 → open
	if c := f.reqs["files.completeUploadExternal"]; len(f.reqs["conversations.open"]) != 1 || c[0].Get("channel_id") != "D2OPENEDDM" || !strings.Contains(c[0].Get("files"), `"title":"T"`) {
		t.Errorf("DM upload: %v", c)
	}
	gslack("upload", "https://acme.slack.com/archives/C1GENERAL0/p1700000002500000?thread_ts=1700000002.000000&cid=C1GENERAL0", path)
	if c := f.reqs["files.completeUploadExternal"]; c[1].Get("thread_ts") != "1700000002.000000" {
		t.Errorf("permalink → 스레드: %v", c[1])
	}
}

func TestDownload(t *testing.T) {
	f := setup(t)
	var evilHit atomic.Bool
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { evilHit.Store(true) }))
	t.Cleanup(evil.Close)
	f.evil = evil.URL
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "report.pdf"), []byte("old"), 0o600)
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }

	if code, out, e := gslack("download", "F1FILEFILE", "-o", dir); code != 0 || read("report (1).pdf") != "DATA-report" || read("report.pdf") != "old" {
		t.Errorf("덮어쓰지 않고 (1): %d %s %s", code, out, e)
	}
	code, out, e := gslack("download", "general", "1700000010.000000", "-o", dir, "--json") // F1 + 외부 F2
	if code != 0 || read("report (2).pdf") != "DATA-report" || !strings.Contains(e, "docs.google.com") || strings.Count(out, `"id"`) != 1 {
		t.Errorf("메시지 파일: %d %s %s", code, out, e)
	}
	if code, _, _ := gslack("download", "F3EVILNAME", "-o", dir); code != 0 || read("evil.txt") != "DATA-evil" {
		t.Errorf("../ 이름은 디렉터리 안으로: %d", code)
	}
	before, _ := os.ReadDir(dir)
	if code, _, e := gslack("download", "F4REDIRECT", "-o", dir); code != 1 || evilHit.Load() || !strings.Contains(e, "Slack 밖으로 리다이렉트하지 않는다") {
		t.Errorf("외부 리다이렉트: %d hit=%v %s", code, evilHit.Load(), e)
	}
	if code, _, _ := gslack("download", "F5PARTIAL0", "-o", dir); code != 1 {
		t.Errorf("중간에 끊긴 다운로드: %d", code)
	}
	if after, _ := os.ReadDir(dir); len(after) != len(before) {
		t.Errorf("실패한 다운로드가 파일을 남겼다: %v", after)
	}
	if code, _, e := gslack("download", "general", "1700000011.000000", "-o", dir); code != 0 || !strings.Contains(e, "F5PARTIAL0 실패") || read("report (3).pdf") != "DATA-report" {
		t.Errorf("하나라도 받으면 성공: %d %s", code, e)
	}
	for _, c := range []struct {
		args []string
		code int
	}{{[]string{"download", "F2EXTERNAL", "-o", dir}, 1}, {[]string{"download", "F1FILEFILE", "-o", filepath.Join(dir, "none")}, 2},
		{[]string{"download", "general", "1700000005.000000", "-o", dir}, 2}} {
		if code, _, _ := gslack(c.args...); code != c.code {
			t.Errorf("%v: %d, want %d", c.args, code, c.code)
		}
	}
}

func TestSendAt(t *testing.T) {
	f := setup(t)
	code, out, e := gslack("send", "general", "later", "--at", "2h", "--thread", "1700000002.000000", "--json")
	if code != 0 || !strings.Contains(out, "Q1SCHEDULE") {
		t.Fatalf("%d %s %s", code, out, e)
	}
	sm := f.reqs["chat.scheduleMessage"][0]
	at, err := strconv.ParseInt(sm.Get("post_at"), 10, 64)
	if want := time.Now().Add(2 * time.Hour).Unix(); err != nil || at < want-5 || at > want+5 || sm.Get("thread_ts") != "1700000002.000000" || sm.Get("text") != "later" {
		t.Errorf("scheduleMessage: %v", sm) // post_at은 Unix 초
	}
	if len(f.reqs["chat.postMessage"]) != 0 {
		t.Error("예약인데 바로 보냈다")
	}
	f.reqs = map[string][]url.Values{}
	for _, at := range []string{"2020-01-01T00:00:00Z", "100ms", "soon"} {
		if code, _, _ := gslack("send", "general", "x", "--at", at); code != 2 || len(f.reqs) != 0 {
			t.Errorf("--at %s: %d", at, code)
		}
	}
	if code, out, _ := gslack("send", "general", "x", "--at", "1d"); code != 0 || !strings.Contains(out, "(Q1SCHEDULE)  ← general") {
		t.Errorf("사람용: %d %s", code, out)
	}
}

func TestRenderGaps(t *testing.T) {
	f := setup(t)
	code, out, e := gslack("read", "C2RENDER00", "-n", "6")
	if code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	for _, want := range []string{
		"@jk:     go test\n", // 앞 빈 줄은 버리고 들여쓰기는 남긴다
		"@jk: @devs @here\n", // rich_text의 그룹·broadcast
		"@jenkins: @devs @subteam build", "@B3GONEGONE: gone", "@B3GONEGONE: again",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("출력에 %q 없음:\n%s", want, out)
		}
	}
	if n, g := len(f.reqs["bots.info"]), len(f.reqs["usergroups.list"]); n != 2 || g != 1 {
		t.Errorf("bots.info %d회(실패도 메모해 2), usergroups.list %d회", n, g)
	}
	if got := preview("\n\n  첫 줄\n둘째", 80); got != "첫 줄" {
		t.Errorf("preview = %q", got)
	}
}

func TestRichTextOnly(t *testing.T) {
	var m slack.Message
	json.Unmarshal([]byte(`{"ts":"1.000001","subtype":"huddle_thread","blocks":[{"type":"rich_text","elements":[
		{"type":"rich_text_section","elements":[{"type":"text","text":"허들이 시작됨 "},{"type":"user","user_id":"U1"},{"type":"link","url":"https://x.com","text":"x"}]},
		{"type":"rich_text_list","style":"bullet","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"하나"}]}]}]}]}`), &m)
	if got, want := fallbackText(m), "허들이 시작됨 <@U1><https://x.com|x>\n• 하나"; got != want {
		t.Errorf("fallbackText = %q; want %q", got, want)
	}
}

func TestWatchKeep(t *testing.T) {
	w := &watcher{me: "UME", member: map[string]bool{"C1": true, "C2": true}, targets: map[string]bool{"C2": true},
		groups: map[string]bool{"SMINE": true}}
	ev := func(ch, typ, user, sub, text, parent string) *slackevents.MessageEvent {
		return &slackevents.MessageEvent{Channel: ch, ChannelType: typ, User: user, SubType: sub, Text: text,
			Message: &slack.Msg{ParentUserId: parent}}
	}
	cases := []struct {
		name        string
		e           *slackevents.MessageEvent
		show, alert bool
	}{
		{"채널 일반", ev("C1", "channel", "U2", "", "hi", ""), false, false},
		{"대상 일반", ev("C2", "channel", "U2", "", "hi", ""), true, false},
		{"DM", ev("D1", "im", "U2", "", "hi", ""), true, true},
		{"그룹 DM", ev("G1", "mpim", "U2", "", "hi", ""), true, true},
		{"내 메시지", ev("D1", "im", "UME", "", "hi", ""), false, false},
		{"수정 이벤트", ev("D1", "im", "", "message_changed", "", ""), false, false},
		{"입장", ev("C1", "channel", "U2", "channel_join", "<@UME>", ""), false, false},
		{"내 멘션", ev("C1", "channel", "U2", "", "hey <@UME>", ""), true, true},
		{"미가입 채널 내 멘션", ev("C9", "channel", "U2", "", "<@UME|jk>", ""), true, true},
		{"남 멘션", ev("C1", "channel", "U2", "", "<@UMEX>", ""), false, false},
		{"here", ev("C1", "channel", "U2", "", "<!here> 공지", ""), true, true},
		{"미가입 channel", ev("C9", "channel", "U2", "", "<!channel>", ""), false, false},
		{"내 그룹", ev("C1", "channel", "U2", "", "<!subteam^SMINE|@dev>", ""), true, true},
		{"남 그룹", ev("C1", "channel", "U2", "", "<!subteam^SOTHER>", ""), false, false},
		{"내 스레드 답글", ev("C1", "channel", "U2", "", "ok", "UME"), true, true},
		{"봇 DM", ev("D1", "im", "", "bot_message", "알림", ""), true, true},
		{"브로드캐스트 통과", ev("C2", "channel", "U2", "thread_broadcast", "hi", ""), true, false},
		{"파일 통과", ev("C2", "channel", "U2", "file_share", "", ""), true, false},
		{"me_message", ev("C2", "channel", "U2", "me_message", "", ""), true, false},
		{"here 라벨", ev("C1", "channel", "U2", "", "<!here|@here>", ""), true, true},
		{"everyone", ev("C1", "channel", "U2", "", "<!everyone>", ""), true, true},
	}
	for _, c := range cases {
		if show, alert := w.keep(c.e, c.e.Text); show != c.show || alert != c.alert {
			t.Errorf("%s: show=%v alert=%v", c.name, show, alert)
		}
	}
	br := ev("C1", "channel", "U2", "thread_broadcast", "ok", "")
	br.Root = &slack.Msg{User: "UME"}
	if show, alert := w.keep(br, "ok"); !show || !alert {
		t.Error("내 스레드의 broadcast 답글을 놓쳤다")
	}
	w.all = true
	if show, alert := w.keep(ev("C1", "channel", "U2", "", "hi", ""), "hi"); !show || alert {
		t.Errorf("--all: %v %v", show, alert)
	}
	if show, _ := w.keep(ev("C9", "channel", "U2", "", "hi", ""), "hi"); show {
		t.Error("--all인데 미가입 채널이 보인다")
	}
	w.groups = nil // usergroups:read 없음 → 모든 그룹 멘션
	if _, alert := w.keep(ev("C1", "channel", "U2", "", "<!subteam^SOTHER>", ""), "<!subteam^SOTHER>"); !alert {
		t.Error("그룹을 모르면 그룹 멘션을 통과시켜야 한다")
	}
}

func TestWatchNeedsAppToken(t *testing.T) {
	f := setup(t)
	t.Setenv("GSLACK_APP_TOKEN", "")
	if code, _, e := gslack("watch"); code != 2 || !strings.Contains(e, "앱 토큰") {
		t.Fatalf("%d %s", code, e)
	}
	if len(f.reqs["users.conversations"]) != 0 {
		t.Error("토큰 검사 전에 대화 목록을 받았다")
	}
}

func TestCompletion(t *testing.T) {
	f := setup(t)
	t.Setenv("GSLACK_TOKEN", "") // 프로필 모드 — env 모드는 완성하지 않는다
	cfg := &store.Config{Current: "work", Profiles: map[string]store.Profile{"work": {}, "home": {}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	c := store.OpenCache("work")
	store.SaveCache(c, "channels", []core.Channel{{ID: "C1", Name: "general", Kind: core.KindPublic},
		{ID: "C2", Name: "gen-ai", Kind: core.KindPrivate}, {ID: "D1", Kind: core.KindIM}, {ID: "G1", Name: "mpdm-a", Kind: core.KindMPIM}})
	store.SaveCache(c, "users", []core.User{{ID: "U1", Name: "jaekwon", DisplayName: "재권"}, {ID: "U2", Name: "jaeold", Deleted: true}})
	cases := []struct {
		args []string
		want string // 후보들 + 마지막 줄 directive
	}{
		{[]string{"read", "gen"}, "general\ngen-ai\n:4"},
		{[]string{"read", "general", ""}, ":4"},   // 둘째 인자 없음
		{[]string{"upload", "general", ""}, ":0"}, // 파일 완성
		{[]string{"whois", "@j"}, "@jaekwon\t재권\n:4"},
		{[]string{"send", "@"}, "@jaekwon\t재권\n:4"},
		{[]string{"watch", "general", "ge"}, "general\ngen-ai\n:4"},
		{[]string{"search", "x", "--from", "@"}, "@jaekwon\t재권\n:4"},
		{[]string{"auth", "use", ""}, "home\nwork\n:4"},
		{[]string{"read", "--profile", "home", "ge"}, ":4"}, // home 캐시 없음
		{[]string{"read", "--profile", ""}, "home\nwork\n:4"},
		{[]string{"search", "x", "--in", "ge"}, "general\ngen-ai\n:4"},
		{[]string{"users", ""}, ":4"},
	}
	for _, tc := range cases {
		_, out, _ := gslack(append([]string{"__complete"}, tc.args...)...)
		if got := strings.TrimSpace(out); got != tc.want {
			t.Errorf("%v:\n%s\nwant\n%s", tc.args, got, tc.want)
		}
	}
	t.Setenv("GSLACK_TOKEN", "xoxp-test")
	if _, out, _ := gslack("__complete", "read", "gen"); strings.TrimSpace(out) != ":4" {
		t.Errorf("env 모드: %q", out)
	}
	if len(f.reqs) != 0 {
		t.Errorf("완성이 API를 불렀다: %v", f.reqs)
	}
}

func TestPickTeam(t *testing.T) {
	teams := []store.SlackTeam{
		{Name: "Acme", URL: "https://acme.slack.com/", Token: "xoxc-1"},
		{Name: "Other Co", URL: "https://otherco.slack.com/", Token: "xoxc-2"},
	}
	// 유일하면 selector 없이 선택.
	if got, err := pickTeam(teams[:1], ""); err != nil || got.Token != "xoxc-1" {
		t.Fatalf("single: %+v err %v", got, err)
	}
	// 여러 개 + selector 없음 → 오류(후보 나열).
	if _, err := pickTeam(teams, ""); err == nil {
		t.Fatal("ambiguous인데 오류 없음")
	}
	// 호스트 접두사 매칭.
	if got, err := pickTeam(teams, "acm"); err != nil || got.Token != "xoxc-1" {
		t.Fatalf("prefix: %+v err %v", got, err)
	}
	// 이름 부분 매칭.
	if got, err := pickTeam(teams, "other co"); err != nil || got.Token != "xoxc-2" {
		t.Fatalf("name: %+v err %v", got, err)
	}
	// 매칭 없음.
	if _, err := pickTeam(teams, "zzz"); err == nil {
		t.Fatal("no match인데 오류 없음")
	}
}

// TestWatchRTM: session 프로필은 앱 토큰 없이 RTM으로 붙고, Socket Mode와 같은 필터를 쓴다.
// DM을 마지막에 보내므로 두 줄(답글, DM)이 나왔으면 앞의 이벤트는 이미 걸러진 것이다.
func TestWatchRTM(t *testing.T) {
	f := setup(t)
	t.Setenv("GSLACK_TOKEN", "xoxc-test")
	t.Setenv("GSLACK_COOKIE", "xoxd-test")
	t.Setenv("GSLACK_APP_TOKEN", "")
	f.rtm = []string{
		`{"type":"user_typing","channel":"D1DMDMDMDM","user":"U1AAAAAAAA"}`,
		`{"type":"message","channel":"D1DMDMDMDM","user":"U0MEMEMEME","text":"내 메시지","ts":"1700000101.000000"}`,
		`{"type":"message","channel":"C9NOTJOIN0","user":"U1AAAAAAAA","text":"미가입","ts":"1700000102.000000"}`,
		`{"type":"message","subtype":"message_changed","hidden":true,"channel":"D1DMDMDMDM","ts":"1700000103.000000",
		  "message":{"user":"U1AAAAAAAA","text":"수정","ts":"1700000001.000000"}}`,
		`{"type":"message","channel":"C1GENERAL0","user":"U1AAAAAAAA","text":"답글","ts":"1700000104.000000",
		  "thread_ts":"1700000002.000000","parent_user_id":"U0MEMEMEME"}`,
		`{"type":"message","channel":"D1DMDMDMDM","user":"U1AAAAAAAA","text":"멘션 없는 DM","ts":"1700000105.000000"}`,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // 필터가 깨져 줄이 모자라도 멈추지 않게
	defer cancel()
	pr, pw := io.Pipe()
	var errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		code := Execute(ctx, []string{"watch", "--json"}, strings.NewReader(""), pw, &errOut)
		pw.Close()
		done <- code
	}()
	sc := bufio.NewScanner(pr)
	var got []core.Message
	for len(got) < 2 && sc.Scan() {
		var m core.Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err, sc.Text())
		}
		got = append(got, m)
	}
	cancel()
	go io.Copy(io.Discard, pr) // 취소 직전에 쓰는 줄이 막히지 않게
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if len(got) != 2 || got[0].Text != "답글" || got[1].Text != "멘션 없는 DM" || got[1].Channel != "D1DMDMDMDM" {
		t.Fatalf("걸러진 결과: %+v", got)
	}
	if len(f.reqs["rtm.connect"]) != 1 {
		t.Errorf("rtm.connect %d회", len(f.reqs["rtm.connect"]))
	}
}

func TestWatchRTMInvalidAuth(t *testing.T) {
	f := setup(t)
	t.Setenv("GSLACK_TOKEN", "xoxc-test")
	t.Setenv("GSLACK_COOKIE", "xoxd-test")
	f.rtmAuth = "invalid_auth"
	if code, _, e := gslack("watch"); code != 1 || !strings.Contains(e, "auth import") {
		t.Fatalf("%d %s", code, e)
	}
	f.rtmAuth = "not_allowed_token_type" // slack-go는 재시도하지만 바뀌지 않는다 — 끝내야 한다
	if code, _, e := gslack("watch", "--json"); code != 1 || !strings.Contains(e, `"error":"not_allowed_token_type"`) {
		t.Fatalf("거절: %d %s", code, e)
	}
	t.Setenv("GSLACK_COOKIE", "")
	if code, _, e := gslack("watch"); code != 2 || !strings.Contains(e, "쿠키") {
		t.Fatalf("쿠키 없음: %d %s", code, e)
	}
}
