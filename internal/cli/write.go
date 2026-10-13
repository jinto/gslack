package cli

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *app) sendCmd() *cobra.Command {
	var thread, at string
	cmd := &cobra.Command{
		Use:   "send <#채널|@사용자|ID|permalink> [본문 | -]",
		Short: "메시지 보내기. permalink를 주면 그 스레드에 답글",
		Long: `본문은 인자 하나, 또는 생략/-이면 stdin. mrkdwn 그대로 보낸다(<@U…> 멘션 가능).
@이름은 정확히 일치하는 사람에게만 보낸다(부분 일치는 거절).
DM 기록이 없는 사용자면 DM을 연다(conversations.open).
네트워크 오류·타임아웃이면 전송 여부가 불확실하다 — 다시 보내기 전에 read로 확인.
--at이면 예약 전송(chat.scheduleMessage). 출력은 예약 ID(Q…) — 목록·취소는 Slack 앱의 "예약됨"에서.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			t, err := core.ParseTarget(args[0])
			if err != nil {
				return usageError{err}
			}
			if thread, err = replyTS(t, thread); err != nil {
				return err
			}
			var when time.Time
			if at != "" {
				now := time.Now()
				if when, err = parseWhen(at, now, 1); err != nil {
					return usagef("--at %v", err)
				}
				if when.Unix() <= now.Unix()+1 { // Slack은 초 단위 — 초 경계를 넘는 100ms도 과거가 되게 1초 여유
					return usagef("--at은 미래여야 한다")
				}
			}
			text, err := a.body(args[1:])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := a.open(ctx)
			if err != nil {
				return err
			}
			ch, err := s.sendChannel(ctx, t)
			if err != nil {
				return err
			}
			opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
			if thread != "" {
				opts = append(opts, slack.MsgOptionTS(thread))
			}
			if at != "" {
				postAt := strconv.FormatInt(when.Unix(), 10) // Slack은 Unix 초를 받는다
				rch, id, err := s.api.ScheduleMessageContext(ctx, ch, postAt, opts...)
				if err != nil {
					return err
				}
				if a.asJSON { // 보낸 메시지가 아직 없으니 send와 모양이 다르다
					return a.printJSON(map[string]any{"channel": rch, "scheduled_message_id": id, "post_at": when.Unix()})
				}
				fmt.Fprintf(a.out, "예약했다 %s (%s)  ← %s\n", when.Local().Format("01/02 15:04"), id, args[0])
				return nil
			}
			rch, _, m, err := s.api.PostMessageWithResponseContext(ctx, ch, opts...)
			if err != nil {
				return err
			}
			if a.asJSON { // 보낸 메시지 하나(배열 아님)
				return a.printJSON(s.message(ctx, m, rch))
			}
			if err := a.show(ctx, s, rch, []slack.Message{m}); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s  ← %s\n", core.Permalink(s.prof.URL, rch, m.Timestamp, m.ThreadTimestamp), args[0])
			return nil
		}),
	}
	cmd.Flags().StringVar(&thread, "thread", "", "이 ts의 스레드에 답글")
	cmd.Flags().StringVar(&at, "at", "", "예약 전송 시각(2h, 1d 또는 RFC3339)")
	return cmd
}

// replyTS는 답글을 달 스레드다: permalink면 그 메시지의 스레드, 아니면 --thread(ts) 또는 없음.
func replyTS(t core.Target, thread string) (string, error) {
	if thread != "" && !core.TSRe.MatchString(thread) {
		return "", usagef("--thread는 Slack ts(1700000000.123456)")
	}
	if t.Kind != core.TargetPermalink {
		return thread, nil
	}
	if thread != "" {
		return "", usagef("permalink와 --thread는 함께 쓸 수 없다")
	}
	return cmp.Or(t.ThreadTS, t.TS), nil
}

// body는 인자 하나 또는 stdin(인자가 `-`이거나 없을 때)에서 본문을 받는다.
func (a *app) body(args []string) (string, error) {
	text := ""
	if len(args) == 1 && args[0] != "-" {
		text = args[0]
	} else {
		if len(args) == 0 && isTTY(a.in) {
			return "", usagef("본문이 없다 — 인자나 stdin으로 준다")
		}
		b, err := io.ReadAll(a.in)
		if err != nil {
			return "", err
		}
		text = string(b)
	}
	if text = strings.TrimSpace(text); text == "" {
		return "", usagef("본문이 비었다")
	}
	return text, nil
}

func (a *app) markCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "mark <#채널|@사용자|ID|permalink>",
		Short: "읽음 처리(기본: 최신 메시지까지, permalink면 그 메시지까지)",
		Long:  "읽음 위치를 과거로 되돌리지 않는다. 터미널이 아니면 --yes가 필요하다.",
		Args:  cobra.ExactArgs(1),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			t, err := core.ParseTarget(args[0])
			if err != nil {
				return usageError{err}
			}
			if t.Kind == core.TargetPermalink && t.ThreadTS != "" {
				return usagef("스레드 답글 링크다 — 부모 메시지 링크나 대화로 지정한다")
			}
			if !yes && !isTTY(a.in) {
				return usagef("확인할 터미널이 없다 — --yes로 실행")
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
			ts := t.TS
			desc := fmt.Sprintf("%s의 %s 메시지", args[0], core.Time(ts).Local().Format("01/02 15:04"))
			if ts == "" {
				res, err := s.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: ch, Limit: 1})
				if err != nil {
					return err
				}
				if len(res.Messages) == 0 {
					return usagef("메시지가 없다")
				}
				m := s.message(ctx, res.Messages[0], ch)
				ts = m.TS
				desc = fmt.Sprintf("%s의 최신 메시지(%s @%s: %s)", args[0], core.Time(ts).Local().Format("01/02 15:04"), m.UserName, preview(m.Text, 40))
			}
			if !yes && !a.confirm(desc+"까지 읽음 처리할까요? [y/N] ") {
				fmt.Fprintln(a.errw, "취소했다")
				return nil
			}
			if err := s.markTo(ctx, ch, ts); err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(map[string]string{"channel": ch, "ts": ts})
			}
			fmt.Fprintf(a.out, "읽음 처리: %s %s\n", args[0], ts)
			return nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "확인 없이 실행")
	return cmd
}

// markTo는 ts까지 읽음 처리한다. 현재 읽음 위치보다 과거면 거절한다(되돌리기 사고 방지).
func (s *session) markTo(ctx context.Context, ch, ts string) error {
	info, err := s.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: ch})
	if err != nil {
		return err
	}
	if ts < info.LastRead { // ts는 고정폭(초 10자리.6자리)이라 문자열 비교로 충분하다
		return usagef("이미 %s까지 읽었다 — 읽음 위치를 과거(%s)로 되돌리지 않는다", info.LastRead, ts)
	}
	return s.api.MarkConversationContext(ctx, ch, ts)
}

// isTTY는 변수다 — 테스트가 확인 프롬프트 경로를 돌리려고 바꾼다.
var isTTY = func(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// confirm은 stderr에 묻고 a.in에서 한 줄을 읽는다. y/yes만 승낙.
func (a *app) confirm(prompt string) bool {
	fmt.Fprint(a.errw, prompt)
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

// preview는 첫 줄을 n자(rune)로 자른다.
func preview(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (a *app) reactCmd() *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "react <permalink | 대상 ts> <emoji>",
		Short: "리액션 달기(--remove면 떼기). 이미 그 상태면 성공으로 본다",
		Args:  cobra.RangeArgs(2, 3),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			t, rest, err := msgRef(args, 1)
			if err != nil {
				return err
			}
			if len(rest) != 1 || strings.Trim(rest[0], ":") == "" {
				return usagef("emoji를 하나 준다(:thumbsup: 또는 thumbsup)")
			}
			emoji := strings.Trim(rest[0], ":") // API는 콜론 없이. :+1::skin-tone-2: → +1::skin-tone-2
			ctx := cmd.Context()
			s, err := a.open(ctx)
			if err != nil {
				return err
			}
			ch, err := s.channelID(ctx, t)
			if err != nil {
				return err
			}
			ref := slack.NewRefToMessage(ch, t.TS)
			if remove {
				err = s.api.RemoveReactionContext(ctx, emoji, ref)
			} else {
				err = s.api.AddReactionContext(ctx, emoji, ref)
			}
			changed := true
			if se := (slack.SlackErrorResponse{}); errors.As(err, &se) && (remove && se.Err == "no_reaction" || !remove && se.Err == "already_reacted") {
				changed, err = false, nil
			}
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(map[string]any{"channel": ch, "ts": t.TS, "emoji": emoji, "reacted": !remove, "changed": changed})
			}
			verb := "달았다"
			switch {
			case remove && changed:
				verb = "뗐다"
			case remove:
				verb = "이미 없다"
			case !changed:
				verb = "이미 달려 있다"
			}
			fmt.Fprintf(a.out, ":%s: %s  %s\n", emoji, verb, core.Permalink(s.prof.URL, ch, t.TS, t.ThreadTS))
			return nil
		}),
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "리액션 떼기")
	return cmd
}

func (a *app) editCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "edit <permalink | 대상 ts> <새 본문>",
		Short: "내 메시지 수정",
		Long:  "본문을 새 text로 바꾸고 blocks(서식)는 지운다. 터미널이 아니면 --yes가 필요하다.",
		Args:  cobra.RangeArgs(2, 3),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			t, rest, err := msgRef(args, 1)
			if err != nil {
				return err
			}
			if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" || rest[0] == "-" {
				return usagef("새 본문을 인자 하나로 준다(stdin은 받지 않는다 — 확인 프롬프트가 stdin을 쓴다)")
			}
			text := strings.TrimSpace(rest[0])
			ctx := cmd.Context()
			s, ch, m, err := a.ownMessage(ctx, t, yes, "→ "+preview(text, 60)+"\n이렇게 수정할까요?")
			if s == nil || err != nil {
				return err
			}
			if _, _, _, err := s.api.UpdateMessageContext(ctx, ch, m.Timestamp,
				slack.MsgOptionText(text, false), slack.MsgOptionBlocks()); err != nil { // blocks를 비워야 앱에 새 본문이 보인다
				return err
			}
			if a.asJSON {
				return a.printJSON(map[string]string{"channel": ch, "ts": m.Timestamp, "text": text})
			}
			fmt.Fprintln(a.out, "수정했다", core.Permalink(s.prof.URL, ch, m.Timestamp, m.ThreadTimestamp))
			return nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "확인 없이 실행")
	return cmd
}

func (a *app) deleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <permalink | 대상 ts>",
		Short: "내 메시지 삭제(되돌릴 수 없다)",
		Long:  "터미널이 아니면 --yes가 필요하다.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			t, _, err := msgRef(args, 0)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, ch, m, err := a.ownMessage(ctx, t, yes, "삭제할까요? 되돌릴 수 없다.")
			if s == nil || err != nil {
				return err
			}
			if _, _, err := s.api.DeleteMessageContext(ctx, ch, m.Timestamp); err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(map[string]string{"channel": ch, "ts": m.Timestamp})
			}
			fmt.Fprintln(a.out, "삭제했다", core.Permalink(s.prof.URL, ch, m.Timestamp, m.ThreadTimestamp))
			return nil
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "확인 없이 실행")
	return cmd
}

// ownMessage는 edit/delete 공통 관문이다: 확인할 수 없으면 Slack 호출 전에 거절하고,
// 원문을 받아 내 메시지인지 확인한 뒤, --yes가 아니면 원문을 보여주고 묻는다.
// 취소하면 session이 nil이다.
func (a *app) ownMessage(ctx context.Context, t core.Target, yes bool, question string) (*session, string, slack.Message, error) {
	var m slack.Message
	if !yes && !isTTY(a.in) {
		return nil, "", m, usagef("확인할 터미널이 없다 — --yes로 실행")
	}
	s, err := a.open(ctx)
	if err != nil {
		return nil, "", m, err
	}
	ch, err := s.channelID(ctx, t)
	if err != nil {
		return nil, "", m, err
	}
	if m, err = s.fetchMessage(ctx, ch, t.TS); err != nil {
		return nil, "", m, err
	}
	// user token의 chat.update/delete는 본인 메시지만 — 봇·시스템 메시지도 거른다(삭제는 되돌릴 수 없다).
	// bot_id로 거르지 않는다: user token으로 보낸 내 메시지에도 앱의 bot_id가 붙는다(2026-10-12 실측). 순수 봇은 user가 비어 걸린다.
	if m.User != s.prof.UserID || (m.SubType != "" && m.SubType != "thread_broadcast") {
		who := cmp.Or(m.BotID, m.SubType)
		if m.User != "" && m.User != s.prof.UserID {
			who = s.userName(ctx, m.User)
		}
		return nil, "", m, usagef("내 메시지만 고치거나 지울 수 있다(%s)", who)
	}
	if !yes {
		s.printMessages(ctx, a.errw, []core.Message{s.message(ctx, m, ch)})
		if !a.confirm(question + " [y/N] ") {
			fmt.Fprintln(a.errw, "취소했다")
			return nil, "", m, nil
		}
	}
	return s, ch, m, nil
}
