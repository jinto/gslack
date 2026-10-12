// gslack — 터미널에서 쓰는 Slack.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/jinto/gslack/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	context.AfterFunc(ctx, stop) // 첫 Ctrl-C는 취소, 두 번째는 기본 동작(즉시 종료) — 토큰 프롬프트에서도 빠져나온다
	code := cli.Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
