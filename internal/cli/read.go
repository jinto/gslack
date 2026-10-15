package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
	"github.com/spf13/cobra"
)

func (a *app) readCmd() *cobra.Command {
	var (
		n             int
		since, before string
		mark          bool
	)
	cmd := &cobra.Command{
		Use:   "read <#채널|@사용자|ID|permalink>",
		Short: "대화의 최근 메시지(오래된 것부터). --mark 없으면 읽음 처리하지 않는다",
		Args:  cobra.ExactArgs(1),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			t, err := core.ParseTarget(args[0])
			if err != nil {
				return usageError{err}
			}
			if t.Kind == core.TargetPermalink && t.ThreadTS != "" {
				return usagef("스레드 답글 링크다 — `gslack thread '%s'`로 읽는다", args[0])
			}
			if n < 1 {
				return usagef("-n은 1 이상")
			}
			p := &slack.GetConversationHistoryParameters{Limit: min(n, 200)}
			if since != "" {
				if p.Oldest, err = parseSince(since, time.Now()); err != nil {
					return usageError{err}
				}
			}
			if before != "" {
				if !core.TSRe.MatchString(before) {
					return usagef("--before는 Slack ts(1700000000.123456)")
				}
				p.Latest = before // 해당 ts는 제외
			}
			if mark && (before != "" || t.Kind == core.TargetPermalink) {
				return usagef("--mark는 최신 메시지를 읽을 때만(--before·permalink와 함께 못 쓴다)")
			}
			if t.Kind == core.TargetPermalink {
				if before != "" {
					return usagef("permalink와 --before는 함께 쓸 수 없다")
				}
				p.Latest, p.Inclusive = t.TS, true // 링크한 메시지까지 포함
			}

			ctx := cmd.Context()
			s, err := a.open(ctx)
			if err != nil {
				return err
			}
			if p.ChannelID, err = s.channelID(ctx, t); err != nil {
				return err
			}
			msgs := []slack.Message{}
			for len(msgs) < n {
				res, err := s.api.GetConversationHistoryContext(ctx, p)
				if err != nil {
					return err
				}
				msgs = append(msgs, res.Messages...)
				if !res.HasMore || res.ResponseMetaData.NextCursor == "" {
					break
				}
				p.Cursor = res.ResponseMetaData.NextCursor
			}
			msgs = msgs[:min(n, len(msgs))]
			newest := ""
			if len(msgs) > 0 {
				newest = msgs[0].Timestamp
			}
			if a.raw {
				err = a.printJSON(msgs) // Slack 원본 그대로(최신순)
			} else {
				slices.Reverse(msgs) // history는 최신순 → 터미널에서는 아래가 최신
				err = a.show(ctx, s, p.ChannelID, msgs)
			}
			if err != nil || !mark || newest == "" {
				return err
			}
			return s.markTo(ctx, p.ChannelID, newest) // 출력 뒤에 — mark가 거절돼도 읽은 내용은 남는다
		}),
	}
	f := cmd.Flags()
	f.IntVarP(&n, "limit", "n", 30, "메시지 수")
	f.StringVar(&since, "since", "", "이 시점 이후(2h, 30m, 3d 또는 RFC3339)")
	f.StringVar(&before, "before", "", "이 ts 이전(해당 ts 제외)")
	f.BoolVar(&mark, "mark", false, "읽은 뒤 최신 메시지까지 읽음 처리")
	return cmd
}

func (a *app) threadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "thread <permalink | 대상 ts>",
		Short: "스레드 전체(부모 + 답글)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			t, _, err := msgRef(args, 0)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := a.open(ctx)
			if err != nil {
				return err
			}
			ch, err := s.channelID(ctx, t)
			if err != nil {
				return err
			}
			msgs, err := s.replies(ctx, ch, cmp.Or(t.ThreadTS, t.TS))
			if err != nil {
				return err
			}
			if a.raw {
				return a.printJSON(msgs)
			}
			return a.show(ctx, s, ch, msgs)
		}),
	}
}

// replies는 스레드 전체(부모 + 답글)를 받는다. API만 부른다(tui가 Cmd 고루틴에서 부른다).
func (s *session) replies(ctx context.Context, ch, ts string) ([]slack.Message, error) {
	p := &slack.GetConversationRepliesParameters{ChannelID: ch, Timestamp: ts, Limit: 200}
	msgs := []slack.Message{}
	for {
		ms, more, next, err := s.api.GetConversationRepliesContext(ctx, p)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, ms...)
		if !more || next == "" {
			return msgs, nil
		}
		p.Cursor = next
	}
}

// show는 오래된 것부터 정렬된 메시지를 정규화해 JSON 또는 사람용으로 출력한다.
func (a *app) show(ctx context.Context, s *session, ch string, msgs []slack.Message) error {
	out := make([]core.Message, len(msgs))
	for i, m := range msgs {
		out[i] = s.message(ctx, m, ch)
	}
	if a.asJSON {
		return a.printJSON(out)
	}
	if err := s.loadChannels(ctx, false); err != nil {
		return err
	}
	s.printMessages(ctx, a.out, out)
	return nil
}

// parseSince는 상대 기간(2h, 3d) 또는 RFC3339를 history의 oldest ts로 바꾼다.
func parseSince(s string, now time.Time) (string, error) {
	t, err := parseWhen(s, now, -1)
	if err != nil {
		return "", fmt.Errorf("--since %w", err)
	}
	return fmt.Sprintf("%d.000000", t.Unix()), nil
}

// parseWhen은 상대 기간(2h, 30m, 3d) 또는 RFC3339를 시각으로 바꾼다. dir은 -1(과거) 또는 +1(미래).
func parseWhen(s string, now time.Time, dir int) (time.Time, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(d)
		if err != nil || days < 0 {
			return time.Time{}, fmt.Errorf("%q: 일수가 아니다", s)
		}
		return now.AddDate(0, 0, dir*days), nil
	}
	if dur, err := time.ParseDuration(s); err == nil && dur >= 0 {
		return now.Add(time.Duration(dir) * dur), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return t, fmt.Errorf("%q: 2h, 30m, 3d 또는 RFC3339", s)
	}
	return t, nil
}

// msgRef는 메시지를 가리키는 인자를 푼다: permalink 하나, 또는 <대상> <ts>. 나머지 인자(최대 max개)는 rest.
func msgRef(args []string, max int) (core.Target, []string, error) {
	t, err := core.ParseTarget(args[0])
	if err != nil {
		return t, nil, usageError{err}
	}
	rest := args[1:]
	if t.Kind != core.TargetPermalink {
		if len(args) < 2 || !core.TSRe.MatchString(args[1]) {
			return t, nil, usagef("메시지는 permalink 하나, 또는 대상과 ts(1700000000.123456)로 지정한다")
		}
		t.TS, rest = args[1], args[2:]
	}
	if len(rest) > max {
		return t, nil, usagef("인자가 너무 많다")
	}
	return t, rest, nil
}

// fetchMessage는 ts 메시지 하나를 받는다. replies는 스레드 답글 ts를 주면 그 답글만 돌려준다.
func (s *session) fetchMessage(ctx context.Context, ch, ts string) (slack.Message, error) {
	ms, _, _, err := s.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: ch, Timestamp: ts, Oldest: ts, Latest: ts, Inclusive: true, Limit: 1})
	if err != nil {
		return slack.Message{}, err
	}
	if i := slices.IndexFunc(ms, func(m slack.Message) bool { return m.Timestamp == ts }); i >= 0 {
		return ms[i], nil
	}
	return slack.Message{}, usagef("%s에 %s 메시지가 없다", ch, ts)
}

// withTags는 본문 뒤에 답글 수·파일·수정 표시를 붙인다.
func withTags(text string, m core.Message) string {
	var tags []string
	if m.ReplyCount > 0 {
		tags = append(tags, fmt.Sprintf("[%d replies]", m.ReplyCount))
	}
	for _, f := range m.Files {
		tags = append(tags, "[file: "+f.Name+"]")
	}
	if m.Edited {
		tags = append(tags, "(edited)")
	}
	if len(tags) > 0 {
		text = strings.TrimSpace(text + " " + strings.Join(tags, " "))
	}
	return text
}

// trimBlankLines는 앞뒤 빈 줄만 버린다(첫 줄 들여쓰기는 남긴다 — 코드 블록).
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	blank := func(l string) bool { return strings.TrimSpace(l) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// printMessages: 날짜가 바뀌면 머리줄, 메시지마다 ts를 항상 앞에 둔다(다음 명령의 입력).
// 메시지마다 첫 줄 번호(쓴 줄 기준 0부터)를 돌려준다 — tui 미리보기가 고른 메시지를 찾는다.
func (s *session) printMessages(ctx context.Context, w io.Writer, msgs []core.Message) []int {
	const indent = "                          " // ts(17) + 2 + HH:MM(5) + 2
	lastDay, n := "", 0
	starts := make([]int, len(msgs))
	for i, m := range msgs {
		t := core.Time(m.TS).Local()
		if day := t.Format("2006-01-02 Mon"); day != lastDay {
			fmt.Fprintf(w, "── %s ──\n", day)
			lastDay = day
			n++
		}
		text := withTags(trimBlankLines(s.render(ctx, m.Text)), m)
		name := m.UserName
		if name == "" {
			name = m.User
		}
		lines := strings.Split(text, "\n")
		fmt.Fprintf(w, "%-17s  %s  @%s: %s\n", m.TS, t.Format("15:04"), name, lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(w, "%s%s\n", indent, l)
		}
		starts[i], n = n, n+len(lines)
	}
	return starts
}
