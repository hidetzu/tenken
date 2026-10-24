// tenken は Web サイトを 1 回の実行で点検する CLI。
// 振る舞いの正本は docs/cli-reference.md。main は cli.Run に任せて、その戻り値で終わるだけ（docs/design.md §2）。
package main

import (
	"os"

	"github.com/hidetzu/tenken/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
