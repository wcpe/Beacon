#!/usr/bin/env bash
# 校验「本次改动」的 Go 文件格式（gofmt + goimports），CRLF 安全。
#
# 为何只查改动文件：本机 autocrlf=true 时工作树为 CRLF，直接 `gofmt -l .` 或
# golangci-lint 的格式化器会把每个文件都误报为「未格式化」；而逐个去 CR 再校验
# 全仓 300+ 文件需在 Windows 上反复起进程、耗时数分钟。提交前真正需要确认的只是
# 「本次要提交的改动」，故此处仅校验工作树相对 HEAD 的改动 + 未跟踪新文件——
# 既快又等价 CI 的 LF 检查（见 .claude/rules/static-analysis.md §2.1）。全仓格式由
# CI 的 `gofmt -l .` 兜底；linter 由 golangci-lint 全模块覆盖（见 Makefile lint 目标）。
#
# 用法：check-go-format.sh [goimports-local-prefix]
#   goimports-local-prefix  goimports -local 的本地导入前缀，省略时取模块路径默认值。
# 退出码：全部通过 0；存在未格式化文件 1。

# 本地导入分组前缀（与 go.mod 模块路径一致）
local_prefix="${1:-github.com/wcpe/Beacon}"

# 本次改动的 .go 文件：工作树相对 HEAD 的差异（含已暂存与未暂存）+ 未跟踪非忽略新文件。
files="$( { git diff --name-only HEAD -- '*.go'; git ls-files --others --exclude-standard '*.go'; } | sort -u )"
if [ -z "$files" ]; then
  echo "无改动的 Go 文件，跳过格式校验。"
  exit 0
fi

# 确保 goimports 可用（CI 亦按需 go install）
if ! command -v goimports >/dev/null 2>&1; then
  echo "未装 goimports，正在安装…"
  go install golang.org/x/tools/cmd/goimports@latest || exit 1
fi
goimports_bin="$(command -v goimports 2>/dev/null || echo "$(go env GOPATH)/bin/goimports")"

bad=0

echo "==== gofmt 格式校验（CRLF 安全，仅本次改动）===="
while IFS= read -r f; do
  [ -n "$f" ] && [ -f "$f" ] || continue
  if [ -n "$(tr -d '\r' < "$f" | gofmt -d 2>/dev/null)" ]; then
    echo "未通过 gofmt: $f"
    bad=1
  fi
done <<< "$files"

echo "==== goimports 导入分组校验（CRLF 安全，仅本次改动）===="
while IFS= read -r f; do
  [ -n "$f" ] && [ -f "$f" ] || continue
  if [ -n "$(tr -d '\r' < "$f" | "$goimports_bin" -local "$local_prefix" -d 2>/dev/null)" ]; then
    echo "未通过 goimports: $f"
    bad=1
  fi
done <<< "$files"

if [ "$bad" -ne 0 ]; then
  echo "存在未通过格式校验的 Go 文件（上方列出），请用 gofmt/goimports 修正。"
  exit 1
fi
echo "gofmt + goimports 全部通过（本次改动）。"
