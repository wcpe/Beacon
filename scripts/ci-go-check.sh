#!/usr/bin/env bash
# 本地复现 CI 的 Go 质量门禁（.github/workflows/ci.yml 的「Go 构建、测试与静态检查」job）。
#
# 为什么需要这个脚本：CI 的检查是**逐条**跑的，一条修完才暴露下一条，来回一趟要几分钟。
# 更麻烦的是本地跑 `make lint` 会因「只查工作树改动」而跳过已提交文件，给出假绿。
# 这个脚本按 CI 的顺序、用 CI 同版本的规则把全部检查跑一遍，一次拿全结果。
#
# Windows 注意：检出时常是 CRLF，而 CI 在 ubuntu 上是 LF。因此在检查前统一去掉 CR，
# 否则 gofmt/goimports 会把「换行符不同」误报成「格式不对」。
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1
FAIL=0

say() { printf '\n=== %s ===\n' "$1"; }
fail() {
	echo "FAIL: $1"
	FAIL=1
}

say "go build ./..."
if ! (cd apps/server && go build ./...); then fail "go build"; fi

say "go test ./..."
if ! (cd apps/server && go test ./...); then fail "go test"; fi

say "go vet -tags e2e ./..."
if ! (cd apps/server && go vet -tags e2e ./...); then fail "go vet e2e"; fi

say "go vet -tags 'e2e loadtest' ./..."
if ! (cd apps/server && go vet -tags "e2e loadtest" ./...); then fail "go vet loadtest"; fi

say "golangci-lint（CI 用 v2.14.0）"
if command -v golangci-lint >/dev/null 2>&1; then
	if ! golangci-lint run ./apps/server/... >/tmp/golangci.out 2>&1; then
		tail -30 /tmp/golangci.out
		fail "golangci-lint"
	else
		echo "0 issues."
	fi
else
	echo "SKIP: 未安装 golangci-lint"
fi

# gofmt 与 goimports：CI 在 LF 工作树上全仓扫描。本地用去 CR 的临时副本等价验证。
say "gofmt（去 CR 后等价 CI 的 LF 检查）"
BAD_GOFMT=""
while IFS= read -r f; do
	[ -n "$f" ] || continue
	t=$(mktemp)
	tr -d '\r' <"$f" >"$t"
	if [ -n "$(gofmt -l "$t")" ]; then BAD_GOFMT="$BAD_GOFMT$f"$'\n'; fi
	rm -f "$t"
done < <(git ls-files '*.go')
if [ -n "$BAD_GOFMT" ]; then
	printf '以下文件未通过 gofmt：\n%s' "$BAD_GOFMT"
	fail "gofmt"
else
	echo "全部合格。"
fi

say "goimports 导入分组（去 CR 后等价 CI 的 LF 检查）"
GOIMPORTS="$(go env GOPATH)/bin/goimports"
if [ -x "$GOIMPORTS" ] || command -v goimports >/dev/null 2>&1; then
	BIN="$GOIMPORTS"
	command -v goimports >/dev/null 2>&1 && BIN="goimports"
	BAD_IMP=""
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		t=$(mktemp)
		tr -d '\r' <"$f" >"$t"
		if [ -n "$("$BIN" -l "$t")" ]; then BAD_IMP="$BAD_IMP$f"$'\n'; fi
		rm -f "$t"
	done < <(git ls-files '*.go')
	if [ -n "$BAD_IMP" ]; then
		printf '以下文件未通过 goimports：\n%s' "$BAD_IMP"
		fail "goimports"
	else
		echo "全部合格。"
	fi
else
	echo "SKIP: 未安装 goimports（go install golang.org/x/tools/cmd/goimports@latest）"
fi

printf '\n'
if [ "$FAIL" -eq 0 ]; then
	echo "全部 Go 检查通过。"
	exit 0
fi
echo "存在未通过的检查（见上）。"
exit 1
