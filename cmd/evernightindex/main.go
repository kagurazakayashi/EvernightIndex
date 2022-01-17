// Command evernightindex 是 EvernightIndex 的程式入口。
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/kagurazakayashi/EvernightIndex/internal/cli"
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
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Main(ctx, cli.BuildInfo{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}, os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}
