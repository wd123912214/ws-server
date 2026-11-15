#!/usr/bin/env bash
# 构建 Linux 发布包：./build.sh 1.0.0
# 产物：dist/ws-relay-<版本>-linux-amd64.tar.gz（含可执行文件、SDK、文档）
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${1:-$(cat VERSION 2>/dev/null || echo dev)}"
ARCH="${ARCH:-amd64}"
NAME="ws-relay-${VERSION}-linux-${ARCH}"
OUT="dist/${NAME}"

go test ./...
rm -rf "$OUT" && mkdir -p "$OUT"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$OUT/ws-relay" .
cp -r sdk docs README.md "$OUT/"
tar -C dist -czf "dist/${NAME}.tar.gz" "$NAME"
echo "已生成 dist/${NAME}.tar.gz"
