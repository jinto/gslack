// Package core는 gslack의 정규화 모델과 순수 함수다. 모든 명령의 --json 출력과
// (나중의) watch·TUI가 같은 타입을 쓴다. Slack SDK에 의존하지 않는다.
package core

import (
	"strconv"
	"strings"
	"time"
)

type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RealName    string `json:"real_name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Title       string `json:"title,omitempty"`
	StatusText  string `json:"status_text,omitempty"`
	StatusEmoji string `json:"status_emoji,omitempty"`
	TZ          string `json:"tz,omitempty"`
	Bot         bool   `json:"bot,omitempty"`
	Deleted     bool   `json:"deleted,omitempty"`
}

// Channel.Kind 값
const (
	KindPublic  = "public"
	KindPrivate = "private"
	KindIM      = "im"
	KindMPIM    = "mpim"
)

type Channel struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind"`
	User   string `json:"user,omitempty"` // im 상대
	Shared bool   `json:"shared,omitempty"`
}

type Message struct {
	Channel    string     `json:"channel"`
	TS         string     `json:"ts"`
	ThreadTS   string     `json:"thread_ts,omitempty"`
	User       string     `json:"user,omitempty"` // 봇이면 bot_id
	UserName   string     `json:"user_name,omitempty"`
	Bot        bool       `json:"bot,omitempty"`
	Text       string     `json:"text"`
	Subtype    string     `json:"subtype,omitempty"`
	ReplyCount int        `json:"reply_count,omitempty"`
	Edited     bool       `json:"edited,omitempty"`
	Files      []File     `json:"files,omitempty"`
	Reactions  []Reaction `json:"reactions,omitempty"`
	Team       string     `json:"team,omitempty"`
	Permalink  string     `json:"permalink,omitempty"`
}

type File struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Mimetype string `json:"mimetype,omitempty"`
}

type Reaction struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Time은 Slack ts("1700000000.123456")를 시각으로 바꾼다. 형식이 틀리면 0 시각.
func Time(ts string) time.Time {
	sec, frac, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}
	}
	us, _ := strconv.ParseInt((frac + "000000")[:6], 10, 64)
	return time.Unix(s, us*1000)
}

// Permalink는 API 호출 없이 메시지 링크를 조립한다. teamURL은 auth.test의 url.
func Permalink(teamURL, channel, ts, threadTS string) string {
	if teamURL == "" || channel == "" || ts == "" {
		return ""
	}
	link := strings.TrimRight(teamURL, "/") + "/archives/" + channel + "/p" + strings.Replace(ts, ".", "", 1)
	if threadTS != "" && threadTS != ts {
		link += "?thread_ts=" + threadTS + "&cid=" + channel
	}
	return link
}
