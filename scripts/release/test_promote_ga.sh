#!/bin/sh
# GA 晋级恢复单元的行为级回归测试，所有远端操作均由伪 gh/git 模拟。

set -eu

script_dir=$(CDPATH= cd -P "$(dirname "$0")" && pwd)
promoter="$script_dir/promote_ga.sh"
work=$(mktemp -d "${TMPDIR:-/tmp}/promote-ga-test.XXXXXX")
trap 'rm -rf "$work"' 0 HUP INT TERM

passed=0
rc_commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
other_commit=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
rc_tag=v1.2.3-rc.1
ga_tag=v1.2.3

fail_test() {
    printf '%s\n' "测试失败：$*" >&2
    exit 1
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

write_fake_commands() {
    fake_bin=$1
    mkdir -p "$fake_bin"
    cat > "$fake_bin/git" <<'EOF'
#!/bin/sh
set -eu
printf 'git' >> "$FAKE_STATE/calls"
for argument in "$@"; do
    printf ' %s' "$argument" >> "$FAKE_STATE/calls"
done
printf '\n' >> "$FAKE_STATE/calls"
IFS= read -r tag_exists < "$FAKE_STATE/tag-exists"
case "${1:-}" in
    ls-remote)
        test "$tag_exists" = true
        ;;
    fetch)
        test "$tag_exists" = true
        ;;
    rev-parse)
        if test "${2:-}" = --verify; then
            ref=${3:-}
        else
            ref=${2:-}
        fi
        case "$ref" in
            "refs/tags/${RC_TAG}^{commit}") printf '%s\n' "$RC_COMMIT" ;;
            "refs/tags/${GA_TAG}^{commit}")
                test "$tag_exists" = true
                IFS= read -r tag_commit < "$FAKE_STATE/tag-commit"
                printf '%s\n' "$tag_commit"
                ;;
            *) exit 2 ;;
        esac
        ;;
    *) exit 2 ;;
esac
EOF
    cat > "$fake_bin/gh" <<'EOF'
#!/bin/sh
set -eu
printf 'gh' >> "$FAKE_STATE/calls"
for argument in "$@"; do
    printf ' %s' "$argument" >> "$FAKE_STATE/calls"
done
printf '\n' >> "$FAKE_STATE/calls"

release_status() {
    IFS= read -r status < "$FAKE_STATE/release-state"
    printf '%s\n' "$status"
}

case "${1:-}" in
    api)
        test "${2:-}" = --method
        test "${3:-}" = POST
        IFS= read -r tag_exists < "$FAKE_STATE/tag-exists"
        test "$tag_exists" = false
        sha=
        while test "$#" -gt 0; do
            case "$1" in
                sha=*) sha=${1#sha=} ;;
            esac
            shift
        done
        test "$sha" = "$RC_COMMIT"
        printf '%s\n' true > "$FAKE_STATE/tag-exists"
        printf '%s\n' "$sha" > "$FAKE_STATE/tag-commit"
        ;;
    release)
        action=${2:-}
        case "$action" in
            view)
                status=$(release_status)
                test "$status" != missing
                jq=
                shift 3
                while test "$#" -gt 0; do
                    if test "$1" = --jq; then
                        jq=$2
                        break
                    fi
                    shift
                done
                if test -n "$jq"; then
                    printf '%s|false|%s\n' "$GA_TAG" "$status"
                fi
                ;;
            download)
                test "$(release_status)" != missing
                target=
                shift 3
                while test "$#" -gt 0; do
                    if test "$1" = --dir; then
                        target=$2
                        break
                    fi
                    shift
                done
                test -n "$target"
                cp "$FAKE_STATE/release-assets"/* "$target"/
                ;;
            create)
                test "$(release_status)" = missing
                IFS= read -r tag_exists < "$FAKE_STATE/tag-exists"
                test "$tag_exists" = true
                mkdir "$FAKE_STATE/release-assets"
                shift 3
                draft=false
                asset_count=0
                while test "$#" -gt 0; do
                    case "$1" in
                        --draft) draft=true; shift ;;
                        --title|--notes-file) shift 2 ;;
                        --verify-tag) shift ;;
                        --*) exit 4 ;;
                        *) cp "$1" "$FAKE_STATE/release-assets"/; asset_count=$((asset_count + 1)); shift ;;
                    esac
                done
                test "$draft" = true
                test "$asset_count" -gt 0
                printf '%s\n' draft > "$FAKE_STATE/release-state"
                ;;
            edit)
                test "$(release_status)" = draft
                printf '%s\n' published > "$FAKE_STATE/release-state"
                ;;
            upload|delete) exit 90 ;;
            *) exit 3 ;;
        esac
        ;;
    *) exit 3 ;;
esac
EOF
    cat > "$fake_bin/make" <<'EOF'
#!/bin/sh
set -eu
printf 'make' >> "$FAKE_STATE/calls"
for argument in "$@"; do
    printf ' %s' "$argument" >> "$FAKE_STATE/calls"
done
printf '\n' >> "$FAKE_STATE/calls"
test "${1:-}" = release-verify-ga
shift
version=
version_file=
rc_tag=
ga_tag=
rc_assets_dir=
assets_dir=
rc_commit=
ga_commit=
for argument in "$@"; do
    case "$argument" in
        VERSION=*) version=${argument#VERSION=} ;;
        RELEASE_VERSION_FILE=*) version_file=${argument#RELEASE_VERSION_FILE=} ;;
        RELEASE_RC_TAG=*) rc_tag=${argument#RELEASE_RC_TAG=} ;;
        RELEASE_GA_TAG=*) ga_tag=${argument#RELEASE_GA_TAG=} ;;
        RELEASE_RC_ASSETS_DIR=*) rc_assets_dir=${argument#RELEASE_RC_ASSETS_DIR=} ;;
        RELEASE_ASSETS_DIR=*) assets_dir=${argument#RELEASE_ASSETS_DIR=} ;;
        RELEASE_RC_COMMIT=*) rc_commit=${argument#RELEASE_RC_COMMIT=} ;;
        RELEASE_GA_COMMIT=*) ga_commit=${argument#RELEASE_GA_COMMIT=} ;;
        *) exit 5 ;;
    esac
done
test "$(tr -d '\r\n' < "$version_file")" = "$version"
test "$rc_assets_dir" != "$assets_dir"
test "$(git rev-parse --verify "refs/tags/${rc_tag}^{commit}")" = "$rc_commit"
test "$(git rev-parse --verify "refs/tags/${ga_tag}^{commit}")" = "$ga_commit"
test "$rc_commit" = "$ga_commit"
if ! diff -r "$rc_assets_dir" "$assets_dir" >/dev/null; then
    printf '%s\n' "发布校验失败：GA 资产大小与 RC 基准不一致" >&2
    exit 1
fi
EOF
    chmod +x "$fake_bin/git" "$fake_bin/gh" "$fake_bin/make"
}

setup_scenario() {
    name=$1
    tag_exists=$2
    release_status=$3
    asset_mode=${4:-same}
    scenario="$work/$name"
    state="$scenario/state"
    mkdir -p "$state"
    printf '%s\n' 1.2.3 > "$scenario/VERSION"
    copy_assets "$work/base-assets" "$scenario/rc-assets"
    : > "$state/calls"
    printf '%s\n' "$tag_exists" > "$state/tag-exists"
    printf '%s\n' "$rc_commit" > "$state/tag-commit"
    printf '%s\n' "$release_status" > "$state/release-state"
    if test "$release_status" != missing; then
        copy_assets "$scenario/rc-assets" "$state/release-assets"
        if test "$asset_mode" = drift; then
            printf '%s\n' 漂移 >> "$state/release-assets/beacon-1.2.3-linux-amd64"
        fi
    fi
    calls="$state/calls"
}

run_scenario() {
    (
        cd "$scenario"
        PATH="$work/bin:$PATH" \
        FAKE_STATE="$state" \
        RC_TAG="$rc_tag" \
        GA_TAG="$ga_tag" \
        RC_COMMIT="$rc_commit" \
        VERSION=1.2.3 \
        GITHUB_REPOSITORY=example/Beacon \
        sh "$promoter"
    )
}

expect_pass() {
    name=$1
    if run_scenario > "$scenario/stdout" 2> "$scenario/stderr"; then
        passed=$((passed + 1))
        return
    fi
    printf '%s\n' "测试失败（应通过）：$name" >&2
    command cat "$scenario/stdout" >&2
    command cat "$scenario/stderr" >&2
    exit 1
}

expect_fail_with() {
    name=$1
    expected=$2
    if run_scenario > "$scenario/stdout" 2> "$scenario/stderr"; then
        fail_test "$name 应拒绝"
    fi
    grep -F -- "$expected" "$scenario/stderr" >/dev/null || {
        command cat "$scenario/stderr" >&2
        fail_test "$name 的错误信息不明确"
    }
    passed=$((passed + 1))
}

assert_log_contains() {
    name=$1
    expected=$2
    grep -F -- "$expected" "$calls" >/dev/null || fail_test "$name 缺少调用：$expected"
    passed=$((passed + 1))
}

assert_log_not_contains() {
    name=$1
    forbidden=$2
    if grep -F -- "$forbidden" "$calls" >/dev/null; then
        fail_test "$name 出现禁止调用：$forbidden"
    fi
    passed=$((passed + 1))
}

assert_log_order() {
    name=$1
    first=$2
    second=$3
    first_line=$(grep -n -F -- "$first" "$calls" | cut -d: -f 1 | command sed -n '1p')
    second_line=$(grep -n -F -- "$second" "$calls" | cut -d: -f 1 | command sed -n '1p')
    test -n "$first_line" && test -n "$second_line" || fail_test "$name 缺少顺序调用"
    test "$first_line" -lt "$second_line" || fail_test "$name 调用顺序错误"
    passed=$((passed + 1))
}

assert_state() {
    name=$1
    expected=$2
    actual=$(cat "$state/release-state")
    test "$actual" = "$expected" || fail_test "$name 状态为 $actual，预期 $expected"
    passed=$((passed + 1))
}

assert_assets_equal() {
    name=$1
    target=$2
    diff -r "$scenario/rc-assets" "$target" >/dev/null || fail_test "$name 资产目录漂移"
    passed=$((passed + 1))
}

assert_no_forbidden_operations() {
    name=$1
    assert_log_not_contains "$name" "gh release upload"
    assert_log_not_contains "$name" "gh release delete"
    assert_log_not_contains "$name" "--clobber"
    assert_log_not_contains "$name" "make build"
    assert_log_not_contains "$name" "make package"
}

write_fake_commands "$work/bin"
make_assets "$work/base-assets"

test_missing() {
    setup_scenario missing false missing
    expect_pass "tag 与 Release 均缺失时恢复"
    assert_log_contains "missing 创建同提交 tag" "gh api --method POST repos/example/Beacon/git/refs"
    assert_log_contains "missing 创建草稿" "gh release create $ga_tag"
    assert_log_contains "missing 草稿参数" "--draft"
    assert_log_order "missing 必须先校验再创建草稿" "make release-verify-ga" "gh release create $ga_tag"
    assert_log_order "missing 必须先创建草稿再公开" "gh release create $ga_tag" "gh release edit $ga_tag"
    assert_state "missing 最终公开" published
    assert_assets_equal "missing 创建前复制并校验" "$scenario/ga-assets"
    assert_assets_equal "missing 公开后回拉复验" "$scenario/published-ga-assets"
    assert_no_forbidden_operations missing
}

test_tag_only() {
    setup_scenario tag-only true missing
    expect_pass "已有同提交 tag 且无 Release 时恢复"
    assert_log_not_contains "已有 tag 不得重建 tag" "gh api --method POST"
    assert_log_contains "已有 tag 创建草稿" "gh release create $ga_tag"
    assert_log_order "已有 tag 必须先校验再创建草稿" "make release-verify-ga" "gh release create $ga_tag"
    assert_state "已有 tag 最终公开" published
    assert_assets_equal "已有 tag 复制 RC 资产" "$scenario/ga-assets"
    assert_no_forbidden_operations tag-only
}

test_draft() {
    setup_scenario draft true draft
    expect_pass "已有 draft 时下载并校验"
    assert_log_contains "draft 下载既有资产" "gh release download $ga_tag --dir ga-assets"
    assert_log_contains "draft 校验独立目录" "RELEASE_ASSETS_DIR=ga-assets"
    assert_log_contains "draft 通过后公开" "gh release edit $ga_tag"
    assert_log_order "draft 必须先下载再校验" "gh release download $ga_tag --dir ga-assets" "make release-verify-ga"
    assert_log_order "draft 必须校验通过后公开" "make release-verify-ga" "gh release edit $ga_tag"
    assert_log_not_contains "draft 不得重建 Release" "gh release create"
    assert_state "draft 最终公开" published
    assert_assets_equal "draft 下载资产未覆盖" "$scenario/ga-assets"
    assert_no_forbidden_operations draft
}

test_published() {
    setup_scenario published true published
    expect_pass "已有 published 时下载并校验"
    assert_log_contains "published 下载既有资产" "gh release download $ga_tag --dir ga-assets"
    assert_log_order "published 必须先下载再校验" "gh release download $ga_tag --dir ga-assets" "make release-verify-ga"
    assert_log_not_contains "published 跳过重复公开" "gh release edit"
    assert_log_not_contains "published 不得重建 Release" "gh release create"
    assert_state "published 保持公开" published
    assert_assets_equal "published 下载资产未覆盖" "$scenario/ga-assets"
    assert_no_forbidden_operations published
}

test_tag_drift() {
    setup_scenario tag-drift true missing
    printf '%s\n' "$other_commit" > "$state/tag-commit"
    expect_fail_with "已有 GA tag commit 不一致" "已有 GA tag 的 peeled commit 与 RC commit 不一致"
    assert_log_not_contains "tag 漂移不得创建 Release" "gh release create"
    assert_log_not_contains "tag 漂移不得移动 tag" "gh api --method POST"
    assert_no_forbidden_operations tag-drift
}

test_release_without_tag() {
    setup_scenario release-without-tag false draft
    expect_fail_with "Release 存在但真实 tag 缺失" "GA Release 已存在但真实 GA tag 缺失"
    assert_log_not_contains "无真实 tag 不得补建" "gh api --method POST"
    assert_log_not_contains "无真实 tag 不得公开" "gh release edit"
    assert_state "无真实 tag 保持草稿" draft
    assert_no_forbidden_operations release-without-tag
}

test_draft_asset_drift() {
    setup_scenario draft-asset-drift true draft drift
    expect_fail_with "draft 资产不一致" "GA 资产大小与 RC 基准不一致"
    assert_log_not_contains "draft 资产漂移不得公开" "gh release edit"
    assert_log_not_contains "draft 资产漂移不得重建" "gh release create"
    assert_state "draft 资产漂移保持草稿" draft
    assert_no_forbidden_operations draft-asset-drift
}

test_published_asset_drift() {
    setup_scenario published-asset-drift true published drift
    expect_fail_with "published 资产不一致" "GA 资产大小与 RC 基准不一致"
    assert_log_not_contains "published 资产漂移不得重复公开" "gh release edit"
    assert_log_not_contains "published 资产漂移不得重建" "gh release create"
    assert_state "published 资产漂移保持公开" published
    assert_no_forbidden_operations published-asset-drift
}

pids=
for test_case in \
    test_missing \
    test_tag_only \
    test_draft \
    test_published \
    test_tag_drift \
    test_release_without_tag \
    test_draft_asset_drift \
    test_published_asset_drift
do
    "$test_case" &
    pids="$pids $!"
done
for pid in $pids; do
    wait "$pid" || fail_test "GA 晋级恢复并行场景失败"
done

printf '%s\n' "GA 晋级恢复行为测试通过：8 个场景"
