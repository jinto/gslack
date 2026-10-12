package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jinto/gslack/internal/core"
	"github.com/slack-go/slack"
	"github.com/spf13/cobra"
)

func (a *app) uploadCmd() *cobra.Command {
	var thread, title, comment string
	cmd := &cobra.Command{
		Use:   "upload <#채널|@사용자|ID|permalink> <파일>",
		Short: "파일 올리기. permalink를 주면 그 스레드에",
		Long:  "대상 해석은 send와 같다(@이름은 정확 일치만, DM이 없으면 연다).",
		Args:  cobra.ExactArgs(2),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			t, err := core.ParseTarget(args[0])
			if err != nil {
				return usageError{err}
			}
			if thread, err = replyTS(t, thread); err != nil {
				return err
			}
			if args[1] == "-" {
				return usagef("stdin 업로드는 받지 않는다 — 파일로 저장한 뒤 올린다")
			}
			f, err := os.Open(args[1])
			if err != nil {
				return usageError{err}
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				return err
			}
			if st.IsDir() || st.Size() == 0 {
				return usagef("%s: 빈 파일이나 디렉터리는 올릴 수 없다", args[1])
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
			// 경로(File) 대신 Reader — slack-go는 경로 업로드에서 Filename을 무시한다
			sum, err := s.files.UploadFileContext(ctx, slack.UploadFileParameters{Reader: f, Filename: filepath.Base(args[1]),
				FileSize: int(st.Size()), Title: title, InitialComment: comment, Channel: ch, ThreadTimestamp: thread})
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(map[string]string{"id": sum.ID, "title": sum.Title, "channel": ch})
			}
			fmt.Fprintf(a.out, "올렸다 %s (%s)  ← %s\n", filepath.Base(args[1]), sum.ID, args[0])
			return nil
		}),
	}
	f := cmd.Flags()
	f.StringVar(&thread, "thread", "", "이 ts의 스레드에")
	f.StringVar(&title, "title", "", "제목(기본: 파일 이름)")
	f.StringVar(&comment, "comment", "", "함께 보낼 메시지")
	return cmd
}

var fileIDRe = regexp.MustCompile(`^F[A-Z0-9]{8,}$`)

func (a *app) downloadCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "download <파일ID | permalink | 대상 ts>",
		Short: "파일 받기(메시지면 그 메시지의 파일 전부)",
		Long: `같은 이름이 있으면 덮어쓰지 않고 "이름 (1).확장자"로 저장한다. 실패하면 받던 파일을 지운다.
외부 파일(구글 드라이브 등)은 건너뛰고 링크를 보여준다. 하나라도 받으면 성공(0), 하나도 못 받으면 1.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: run(func(cmd *cobra.Command, args []string) error {
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				return usagef("-o %s: 디렉터리가 아니다", dir)
			}
			var (
				ids []string
				t   core.Target
				err error
			)
			if len(args) == 1 && fileIDRe.MatchString(args[0]) { // F…는 채널 이름 규칙에 걸리므로 먼저 본다
				ids = args
			} else if t, _, err = msgRef(args, 0); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := a.open(ctx)
			if err != nil {
				return err
			}
			if ids == nil {
				ch, err := s.channelID(ctx, t)
				if err != nil {
					return err
				}
				m, err := s.fetchMessage(ctx, ch, t.TS)
				if err != nil {
					return err
				}
				for _, f := range m.Files {
					ids = append(ids, f.ID)
				}
				if len(ids) == 0 {
					return usagef("메시지에 파일이 없다")
				}
			}
			type saved struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Path string `json:"path"`
				Size int64  `json:"size"`
			}
			out := []saved{}
			var lastErr error
			for _, id := range ids {
				f, _, _, err := s.api.GetFileInfoContext(ctx, id, 1, 1)
				// 외부 파일(구글 드라이브 등)과 hidden_by_limit(무료 플랜)은 받을 URL이 없다
				if err == nil && (f.IsExternal || f.URLPrivateDownload == "") {
					fmt.Fprintf(a.errw, "gslack: 건너뜀 %s — 외부 파일이거나 받을 수 없다: %s\n", cmp.Or(f.Name, id), f.Permalink)
					continue
				}
				var path string
				var n int64
				if err == nil {
					path, n, err = s.save(ctx, dir, f)
				}
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Fprintf(a.errw, "gslack: %s 실패: %v\n", id, err)
					lastErr = err
					continue
				}
				out = append(out, saved{f.ID, f.Name, path, n})
			}
			if len(out) == 0 {
				return cmp.Or(lastErr, errors.New("받은 파일이 없다"))
			}
			if a.asJSON {
				return a.printJSON(out)
			}
			for _, f := range out {
				fmt.Fprintln(a.out, f.Path)
			}
			return nil
		}),
	}
	cmd.Flags().StringVarP(&dir, "output", "o", ".", "저장할 디렉터리")
	return cmd
}

// save는 dir에 새 이름으로 파일을 만들어(O_EXCL — 덮어쓰지 않는다) 받는다. 실패하면 그 파일을 지운다.
func (s *session) save(ctx context.Context, dir string, f *slack.File) (string, int64, error) {
	name := filepath.Base(f.Name) // Slack이 준 이름의 ../ 등은 버린다
	if name == "." || name == ".." || name == "/" {
		name = f.ID
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	var (
		w    *os.File
		path string
		err  error
	)
	for i := 0; ; i++ {
		if i == 1000 {
			return "", 0, fmt.Errorf("%s: 같은 이름이 너무 많다", name)
		}
		path = filepath.Join(dir, name)
		if i > 0 {
			path = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		// 0600: Slack의 비공개 파일이라 소유자만
		if w, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		return "", 0, err
	}
	err = s.files.GetFileContext(ctx, f.URLPrivateDownload, w)
	st, serr := w.Stat()
	err = cmp.Or(err, serr, w.Close())
	if err != nil {
		os.Remove(path)
		return "", 0, err
	}
	return path, st.Size(), nil
}
