#!/bin/sh
# 发布校验脚本的 shell 契约测试。

set -eu

script_dir=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
root_dir=$(CDPATH= cd -P "$script_dir/../.." && pwd)
checker="$script_dir/release_check.sh"
work=$(mktemp -d "${TMPDIR:-/tmp}/release-test.XXXXXX")
trap 'rm -rf "$work"' 0 HUP INT TERM

passed=0

expect_pass() {
    name=$1
    shift
    if "$@" >"$work/stdout" 2>"$work/stderr"; then
        passed=$((passed + 1))
        return
    fi
    printf '%s\n' "测试失败（应通过）：$name" >&2
    printf '%s\n' "标准输出：" >&2
    command cat "$work/stdout" >&2
    printf '%s\n' "错误输出：" >&2
    command cat "$work/stderr" >&2
    exit 1
}

expect_fail() {
    name=$1
    shift
    if "$@" >"$work/stdout" 2>"$work/stderr"; then
        printf '%s\n' "测试失败（应拒绝）：$name" >&2
        exit 1
    fi
    passed=$((passed + 1))
}

expect_fail_with() {
    name=$1
    expected=$2
    shift 2
    if "$@" >"$work/stdout" 2>"$work/stderr"; then
        printf '%s\n' "测试失败（应拒绝）：$name" >&2
        exit 1
    fi
    if ! grep -F -- "$expected" "$work/stderr" >/dev/null; then
        printf '%s\n' "测试失败（错误信息不明确）：$name" >&2
        command cat "$work/stderr" >&2
        exit 1
    fi
    passed=$((passed + 1))
}

expect_file_contains() {
    name=$1
    file=$2
    expected=$3
    if grep -F -- "$expected" "$file" >/dev/null; then
        passed=$((passed + 1))
        return
    fi
    printf '%s\n' "测试失败（缺少工作流约束）：$name" >&2
    exit 1
}

expect_file_not_contains() {
    name=$1
    file=$2
    forbidden=$3
    if grep -F -- "$forbidden" "$file" >/dev/null; then
        printf '%s\n' "测试失败（工作流含禁止操作）：$name" >&2
        exit 1
    fi
    passed=$((passed + 1))
}

run_in_repo() {
    repository=$1
    shift
    (
        cd "$repository"
        "$@"
    )
}

hash_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        output=$(sha256sum "$1")
    else
        output=$(shasum -a 256 "$1")
    fi
    printf '%s\n' "${output%% *}"
}

asset_names() {
    printf '%s\n' \
        beacon-1.2.3-linux-amd64 \
        beacon-1.2.3-linux-arm64 \
        beacon-1.2.3-windows-amd64.exe \
        beacon-1.2.3-darwin-arm64 \
        BeaconAgent-1.2.3.jar \
        BeaconAgentProxy-1.2.3.jar \
        beacon-maven-1.2.3.zip
}

refresh_sums() {
    directory=$1
    : > "$directory/SHA256SUMS.txt"
    asset_names | while IFS= read -r name; do
        printf '%s  %s\n' "$(hash_file "$directory/$name")" "$name" >> "$directory/SHA256SUMS.txt"
    done
}

make_assets() {
    directory=$1
    mkdir -p "$directory"
    asset_names | while IFS= read -r name; do
        printf '%s\n' "$name" > "$directory/$name"
    done
    refresh_sums "$directory"
}

copy_assets() {
    source_dir=$1
    target_dir=$2
    mkdir -p "$target_dir"
    cp "$source_dir"/* "$target_dir"/
}

verify_rc() {
    repository=$1
    assets=$2
    commit=$3
    tag=${4:-v1.2.3-rc.1}
    run_in_repo "$repository" sh "$checker" verify-rc \
        --version-file "$work/VERSION" \
        --version 1.2.3 \
        --rc-tag "$tag" \
        --ga-tag v1.2.3 \
        --assets-dir "$assets" \
        --rc-commit "$commit"
}

verify_ga() {
    repository=$1
    rc_assets=$2
    ga_assets=$3
    rc_identity=$4
    ga_identity=$5
    rc_tag=${6:-v1.2.3-rc.1}
    run_in_repo "$repository" sh "$checker" verify-ga \
        --version-file "$work/VERSION" \
        --version 1.2.3 \
        --rc-tag "$rc_tag" \
        --ga-tag v1.2.3 \
        --rc-assets-dir "$rc_assets" \
        --assets-dir "$ga_assets" \
        --rc-commit "$rc_identity" \
        --ga-commit "$ga_identity"
}

printf '%s\n' 1.2.3 > "$work/VERSION"

expect_pass "静态 check 不依赖真实 tag" sh "$checker" check \
    --version-file "$work/VERSION" \
    --version 1.2.3 \
    --rc-tag v1.2.3-rc.999 \
    --ga-tag v1.2.3 \
    --workflow "$root_dir/.github/workflows/release.yml"
for tag in v1.2.3-rc.0 v1.2.3-rc.01 v1.2.3-rc v1.2.3-rc.1-extra v01.2.3-rc.1; do
    expect_fail "非法 RC tag：$tag" sh "$checker" check \
        --version-file "$work/VERSION" --version 1.2.3 --rc-tag "$tag" --ga-tag v1.2.3
done
expect_fail "非法 GA tag" sh "$checker" check \
    --version-file "$work/VERSION" --version 1.2.3 --rc-tag v1.2.3-rc.1 --ga-tag v1.2.4
expect_fail "VERSION mismatch" sh "$checker" check \
    --version-file "$work/VERSION" --version 1.2.4 --rc-tag v1.2.4-rc.1 --ga-tag v1.2.4
expect_fail "静态 check 拒绝提交身份参数" sh "$checker" check \
    --version-file "$work/VERSION" --version 1.2.3 --rc-tag v1.2.3-rc.1 --ga-tag v1.2.3 \
    --rc-commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

repository="$work/repository"
git init -q "$repository"
git -C "$repository" config user.name "发布测试"
git -C "$repository" config user.email "release-test@example.invalid"
empty_tree=$(git -C "$repository" mktree </dev/null)
commit_one=$(printf '%s\n' "第一个测试提交" | git -C "$repository" commit-tree "$empty_tree")
commit_two=$(printf '%s\n' "第二个测试提交" | git -C "$repository" commit-tree "$empty_tree" -p "$commit_one")
git -C "$repository" tag -a v1.2.3-rc.1 "$commit_one" -m "测试 RC"
git -C "$repository" tag -a v1.2.3 "$commit_one" -m "测试 GA"
git -C "$repository" tag -a v1.2.3-rc.2 "$commit_two" -m "漂移 RC"

make_assets "$work/assets-rc"
expect_pass "RC 从真实 annotated tag 解析 peeled commit" verify_rc \
    "$repository" "$work/assets-rc" "$commit_one"
expect_pass "资产校验默认读取 VERSION" run_in_repo "$repository" sh "$checker" verify-rc \
    --version-file "$work/VERSION" \
    --rc-tag v1.2.3-rc.1 \
    --ga-tag v1.2.3 \
    --assets-dir "$work/assets-rc" \
    --rc-commit "$commit_one"
expect_fail "手工 SHA 不能绕过真实 RC tag 身份" verify_rc \
    "$repository" "$work/assets-rc" "$commit_two"
expect_fail "缺失真实 RC tag" verify_rc \
    "$repository" "$work/assets-rc" "$commit_one" v1.2.3-rc.404

copy_assets "$work/assets-rc" "$work/assets-missing"
rm "$work/assets-missing/BeaconAgent-1.2.3.jar"
expect_fail "缺少资产" verify_rc "$repository" "$work/assets-missing" "$commit_one"

copy_assets "$work/assets-rc" "$work/assets-extra"
printf '%s\n' extra > "$work/assets-extra/unlisted.bin"
expect_fail "多出资产" verify_rc "$repository" "$work/assets-extra" "$commit_one"

copy_assets "$work/assets-rc" "$work/assets-duplicate"
first_line=$(command sed -n '1p' "$work/assets-duplicate/SHA256SUMS.txt")
printf '%s\n' "$first_line" >> "$work/assets-duplicate/SHA256SUMS.txt"
expect_fail "重复校验项" verify_rc "$repository" "$work/assets-duplicate" "$commit_one"

copy_assets "$work/assets-rc" "$work/assets-bad-hash"
command sed '1s/^[0-9a-f][0-9a-f]*/xyz/' "$work/assets-bad-hash/SHA256SUMS.txt" > "$work/bad-sums"
mv "$work/bad-sums" "$work/assets-bad-hash/SHA256SUMS.txt"
expect_fail "坏 hash" verify_rc "$repository" "$work/assets-bad-hash" "$commit_one"

copy_assets "$work/assets-rc" "$work/assets-traversal"
printf '%064d  %s\n' 0 '../escape.bin' >> "$work/assets-traversal/SHA256SUMS.txt"
expect_fail "路径穿越" verify_rc "$repository" "$work/assets-traversal" "$commit_one"

copy_assets "$work/assets-rc" "$work/assets-ga"
expect_pass "GA 从真实 tag 校验同提交与 RC 资产基准" verify_ga \
    "$repository" "$work/assets-rc" "$work/assets-ga" "$commit_one" "$commit_one"
same_directory_error="RC 基准资产目录与 GA 资产目录必须指向不同的真实目录"
expect_fail_with "RC 与 GA 直接复用同一资产目录" "$same_directory_error" verify_ga \
    "$repository" "$work/assets-rc" "$work/assets-rc" "$commit_one" "$commit_one"
copy_assets "$work/assets-rc" "$repository/assets-relative"
expect_fail_with "RC 相对路径与 GA 绝对路径归一后相同" "$same_directory_error" verify_ga \
    "$repository" assets-relative "$repository/assets-relative" "$commit_one" "$commit_one"
if MSYS=winsymlinks:sys ln -s "$work/assets-rc" "$work/assets-rc-link" 2>/dev/null && test -L "$work/assets-rc-link"; then
    expect_fail_with "RC 与 GA 符号链接归一后相同" "$same_directory_error" verify_ga \
        "$repository" "$work/assets-rc" "$work/assets-rc-link" "$commit_one" "$commit_one"
else
    printf '%s\n' "跳过符号链接目录反例：当前平台不允许创建真实符号链接"
fi
expect_fail "手工 GA SHA 不能绕过真实 GA tag 身份" verify_ga \
    "$repository" "$work/assets-rc" "$work/assets-ga" "$commit_one" "$commit_two"
expect_fail "真实 RC tag 漂移时拒绝给定旧身份" verify_ga \
    "$repository" "$work/assets-rc" "$work/assets-ga" "$commit_one" "$commit_one" v1.2.3-rc.2

copy_assets "$work/assets-rc" "$work/assets-ga-size-drift"
printf '%s\n' drift >> "$work/assets-ga-size-drift/beacon-1.2.3-linux-amd64"
refresh_sums "$work/assets-ga-size-drift"
expect_fail "GA 资产与自己的校验和一起发生大小漂移" verify_ga \
    "$repository" "$work/assets-rc" "$work/assets-ga-size-drift" "$commit_one" "$commit_one"

copy_assets "$work/assets-rc" "$work/assets-ga-hash-drift"
printf '%s' X | dd of="$work/assets-ga-hash-drift/beacon-1.2.3-linux-arm64" bs=1 seek=0 conv=notrunc 2>/dev/null
refresh_sums "$work/assets-ga-hash-drift"
expect_fail "GA 资产与自己的校验和一起发生 SHA-256 漂移" verify_ga \
    "$repository" "$work/assets-rc" "$work/assets-ga-hash-drift" "$commit_one" "$commit_one"

copy_assets "$work/assets-rc" "$work/assets-ga-sums-drift"
{
    command sed -n '2,$p' "$work/assets-ga-sums-drift/SHA256SUMS.txt"
    command sed -n '1p' "$work/assets-ga-sums-drift/SHA256SUMS.txt"
} > "$work/reordered-sums"
mv "$work/reordered-sums" "$work/assets-ga-sums-drift/SHA256SUMS.txt"
expect_fail "GA 校验和文件本身必须与 RC 基准逐字节一致" verify_ga \
    "$repository" "$work/assets-rc" "$work/assets-ga-sums-drift" "$commit_one" "$commit_one"

workflow="$root_dir/.github/workflows/release.yml"
promoter="$script_dir/promote_ga.sh"
expect_pass "release.yml 的 GA job 不含构建打包重签命令" sh "$checker" audit-ga-workflow \
    --workflow "$workflow"
expect_file_not_contains "workflow 的 GA job 不要求 production 环境审批" "$workflow" \
    'environment: production'
expect_file_contains "workflow 必须调用 GA 晋级恢复单元" "$workflow" \
    'run: sh scripts/release/promote_ga.sh'
expect_file_not_contains "workflow 不得保留静态 GA 恢复分支" "$workflow" \
    'ga_release_state='
expect_file_not_contains "GA 恢复不得覆盖既有资产" "$promoter" '--clobber'
expect_file_not_contains "GA 恢复不得上传替换既有资产" "$promoter" 'gh release upload'
expect_file_not_contains "GA 恢复不得删除既有 Release" "$promoter" 'gh release delete'
printf '%s\n' 'jobs:' '  release:' '    steps:' '      - run: true' > "$work/good-release.yml"
expect_pass "GA job 无审批环境也可通过静态断言" sh "$checker" audit-ga-workflow \
    --workflow "$work/good-release.yml"
printf '%s\n' 'jobs:' '  release:' '    environment: production' '    steps:' '      - run: true' > "$work/optional-production-release.yml"
expect_pass "GA job 即使残留 production 声明也不作为发布条件" sh "$checker" audit-ga-workflow \
    --workflow "$work/optional-production-release.yml"
printf '%s\n' 'jobs:' '  release:' '    steps:' '      - run: go build ./...' > "$work/bad-build-release.yml"
expect_fail "GA job 构建命令静态断言" sh "$checker" audit-ga-workflow \
    --workflow "$work/bad-build-release.yml"
printf '%s\n' 'jobs:' '  release:' '    steps:' '      - run: cosign sign-blob product.bin' > "$work/bad-sign-release.yml"
expect_fail "GA job 重签命令静态断言" sh "$checker" audit-ga-workflow \
    --workflow "$work/bad-sign-release.yml"

printf '%s\n' "发布校验契约测试通过：$passed 项"
