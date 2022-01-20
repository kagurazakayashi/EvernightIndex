// Command evernightindex 是 EvernightIndex 的程式入口。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/kagurazakayashi/EvernightIndex/internal/cli"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 版本資訊由建置流程以 -ldflags 注入，例如：
//
//	go build -ldflags "-X main.version=0.1.0 -X main.commit=$(git rev-parse --short HEAD)" ./cmd/evernightindex
var (
	version   = "0.1.0-dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// main 只做三件事：接上中斷訊號、組出建置資訊、把退出碼交給系統。
//
// 中斷分兩段：第一次請求取消，交由 cli 與 app 停止調度、等待受控清理並輸出
// 最終統計；第二次直接結束（退出碼 130），因為此時可能已經卡在無法取消的
// 系統呼叫上。第二次結束不會清除暫時檔或偽造結果，未提交的交易由 SQLite 回滾，
// 已提交的結果保持已提交。
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		if _, ok := <-signals; !ok {
			return
		}
		cancel()
		if _, ok := <-signals; !ok {
			return
		}
		fmt.Fprintln(os.Stderr,
			"evernightindex: E_INTERRUPTED: second interrupt, exiting immediately; an unfinished run is left unpublished")
		os.Exit(int(model.ExitInterrupted))
	}()

	os.Exit(cli.Main(ctx, cli.BuildInfo{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}, os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}
