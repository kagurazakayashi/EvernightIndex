#!/usr/bin/env sh
# EvernightIndex 的自動驗證：格式檢查、go vet、單元測試與三個平台的交叉編譯。
#
# 用法：scripts/verify.sh
#   JOBS=1          限制 go 套件並行度（預設 2）
#   GOMAXPROCS=4    限制編譯與測試使用的執行緒數（預設 4）
#   OUT=<dir>       交叉編譯輸出目錄（預設 .build-out，已在 .gitignore 中）
#
# 注意：若 `go env GOTMPDIR` 指向不存在的目錄，先設定 GOTMPDIR 指向可寫目錄，
# 否則 go 會在 "creating work dir" 階段失敗。
#
# 本腳本只做本地建置與測試：不掃描使用者資料，不建立正式資料庫，
# 也不執行 git add/commit/push。
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

JOBS=${JOBS:-2}
export GOMAXPROCS=${GOMAXPROCS:-4}
OUT=${OUT:-$ROOT/.build-out}

echo "== gofmt"
UNFORMATTED=$(gofmt -l ./cmd ./internal)
if [ -n "$UNFORMATTED" ]; then
    echo "unformatted files:"
    echo "$UNFORMATTED"
    exit 1
fi

echo "== go vet"
go vet ./...

echo "== go test"
go test -timeout 180s -count=1 -p "$JOBS" ./...

echo "== cross-compile (CGO_ENABLED=0)"
mkdir -p "$OUT"
for TARGET in windows/amd64 darwin/arm64 darwin/amd64 linux/amd64; do
    GOOS=${TARGET%%/*}
    GOARCH=${TARGET##*/}
    echo "  $TARGET"
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -p "$JOBS" -o "$OUT/evernightindex-$GOOS-$GOARCH" ./cmd/evernightindex
done

echo "artifacts written to $OUT"
