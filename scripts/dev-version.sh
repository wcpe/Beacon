#!/usr/bin/env bash
# 计算滚动预发布 dev 版本号（FR-117，取代旧「最新正式 minor+1 + -dev.<sha>」策略）。
#
# 旧策略以「最新正式 tag 的 minor+1」作基线，使 dev 恒比上个正式版高一位——
# 发布正式版这一动作本身就凭空制造一个更高的 dev，导致预发布渠道「一直有更新」。
#
# 新策略让 dev 版本号反映「真实主干进度」：
#   基线 = 最新正式版 tag 的版本号（不再 +1）；
#   序号 = 自该 tag 起的提交距离（git rev-list --count）；
#   输出 = <基线>-dev.<提交距离>.g<短SHA>（不带前导 v，供注入 Go ldflags 的 Version 变量）。
# 语义版本上 <基线>-dev.* 介于「已发布的 <基线>」与「下个正式版」之间，既不倒挂、
# 又能用「提交距离序号」区分「主干真有新提交」与「仅短 SHA 改写」（in-app hasUpdate 据此判定）。
#
# 提交距离为 0（HEAD 正是该 tag 所指提交、主干无新提交）时退出码 1 且不输出版本号，
# 由调用方（_build-release.yml 的 meta job）据此跳过发布，避免「发完正式版后无新提交仍滚动 dev」。
#
# 用法：dev-version.sh [latest-tag] [count-override] [sha-override]
#   latest-tag    省略时用 git describe 推导最新正式版 tag；
#   count-override 省略时用 git rev-list --count <tag>..HEAD（测试可传入以解耦 git 状态）；
#   sha-override  省略时用 git rev-parse 取 HEAD 7 位短 SHA。
# 输出：完整 dev 版本号（如 0.17.0-dev.3.g6b6dd71）。
set -euo pipefail

# 最新正式版 tag：仅匹配 vX.Y.Z（排除 -rc 等预发布 tag）；取不到则退回 v0.0.0（基线 0.0.0）。
tag="${1:-$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || echo v0.0.0)}"
base="${tag#v}"

# 提交距离：显式传入优先；否则有该 tag 时按 tag..HEAD 计数，无 tag（v0.0.0）时退回总提交数。
if [ "${2:-}" != "" ]; then
  count="$2"
elif git rev-parse -q --verify "refs/tags/${tag}" >/dev/null 2>&1; then
  count="$(git rev-list --count "${tag}..HEAD" 2>/dev/null || echo 0)"
else
  count="$(git rev-list --count HEAD 2>/dev/null || echo 0)"
fi

# 提交距离为 0：主干无新提交，无可发布的 dev，退出码 1（调用方据此跳过发布）。
if [ "$count" -eq 0 ]; then
  echo "提交距离为 0：主干自 ${tag} 起无新提交，跳过 dev 预发布。" >&2
  exit 1
fi

# 7 位短 SHA：显式传入优先，否则取 HEAD。
sha="${3:-$(git rev-parse --short=7 HEAD 2>/dev/null || echo 0000000)}"

echo "${base}-dev.${count}.g${sha}"
