#!/bin/sh
# 通用发布校验工具：校验版本、标签、资产闭集、提交一致性和 GA 工作流。

set -eu

fail() {
    printf '%s\n' "发布校验失败：$*" >&2
    exit 1
}

usage() {
    printf '%s\n' "用法：$0 {check|verify-rc|verify-ga|audit-ga-workflow} 参数" >&2
    exit 2
}

need_value() {
    test -n "${2:-}" || fail "参数 $1 不能为空"
}

is_decimal() {
    test -n "$1" || return 1
    case "$1" in *[!0-9]*) return 1 ;; esac
    return 0
}

valid_version() {
    value=$1
    old_ifs=$IFS
    IFS=.
    set -- $value
    IFS=$old_ifs
    test "$#" -eq 3 || return 1
    test "$1.$2.$3" = "$value" || return 1
    for part in "$@"; do
        is_decimal "$part" || return 1
        case "$part" in 0|[1-9]*) ;; *) return 1 ;; esac
    done
}

read_version() {
    version_file=$1
    test -f "$version_file" || fail "VERSION 文件不存在：$version_file"
    version=$(tr -d '\r' < "$version_file")
    valid_version "$version" || fail "VERSION 必须且只能包含正式 X.Y.Z 版本"
    raw=$({ command cat "$version_file"; printf x; })
    raw=${raw%x}
    carriage=$(printf '\r')
    newline='
'
    case "$raw" in
        "$version"|"$version$newline"|"$version$carriage$newline") ;;
        *) fail "VERSION 必须且只能包含正式 X.Y.Z 版本" ;;
    esac
    printf '%s\n' "$version"
}

valid_sha() {
    value=$1
    test "${#value}" -eq 40 || return 1
    case "$value" in *[!0-9a-f]*) return 1 ;; esac
}

check_tags() {
    version=$1
    rc_tag=$2
    ga_tag=$3
    valid_version "$version" || fail "版本不是正式 X.Y.Z：$version"
    prefix="v${version}-rc."
    case "$rc_tag" in "$prefix"*) ;; *) fail "RC tag 必须严格匹配 v${version}-rc.N：$rc_tag" ;; esac
    sequence=${rc_tag#"$prefix"}
    is_decimal "$sequence" || fail "RC tag 必须严格匹配 v${version}-rc.N：$rc_tag"
    case "$sequence" in 0|0*) fail "RC tag 必须严格匹配 v${version}-rc.N：$rc_tag" ;; esac
    test "$ga_tag" = "v$version" || fail "GA tag 必须严格匹配 v${version}：$ga_tag"
}

hash_command() {
    if command -v sha256sum >/dev/null 2>&1; then
        printf '%s\n' sha256sum
    elif command -v shasum >/dev/null 2>&1; then
        printf '%s\n' shasum
    else
        fail "缺少 sha256sum 或 shasum"
    fi
}

file_hash() {
    command_name=$(hash_command)
    if test "$command_name" = sha256sum; then
        output=$(sha256sum "$1")
    else
        output=$(shasum -a 256 "$1")
    fi
    printf '%s\n' "${output%% *}"
}

file_size() {
    set -- $(wc -c < "$1")
    printf '%s\n' "$1"
}

valid_hash() {
    value=$1
    test "${#value}" -eq 64 || return 1
    case "$value" in *[!0-9a-f]*) return 1 ;; esac
}

contains_line() {
    wanted=$1
    list=$2
    while IFS= read -r line; do
        test "$line" = "$wanted" && return 0
    done < "$list"
    return 1
}

valid_asset_name() {
    name=$1
    test -n "$name" || return 1
    case "$name" in
        */*|*\\*|.|..|../*|*/..|*/../*|/*|[A-Za-z]:*) return 1 ;;
    esac
    return 0
}

expected_asset_names() {
    version=$1
    printf '%s\n' \
        "beacon-${version}-linux-amd64" \
        "beacon-${version}-linux-arm64" \
        "beacon-${version}-windows-amd64.exe" \
        "beacon-${version}-darwin-arm64" \
        "BeaconAgent-${version}.jar" \
        "BeaconAgentProxy-${version}.jar" \
        "beacon-maven-${version}.zip"
}

check_assets() {
    check_dir=$1
    asset_version=$2
    test -d "$check_dir" || fail "资产目录不存在：$check_dir"
    sums="$check_dir/SHA256SUMS.txt"
    test -f "$sums" || fail "资产目录缺少 SHA256SUMS.txt"
    work=$(mktemp -d "${TMPDIR:-/tmp}/release-check.XXXXXX") || fail "无法创建临时目录"
    trap 'rm -rf "$work"' 0 HUP INT TERM
    actual="$work/actual"
    listed="$work/listed"
    expected="$work/expected"
    : > "$actual"
    : > "$listed"
    expected_asset_names "$asset_version" | sort > "$expected"

    find "$check_dir" -type d -print | while IFS= read -r path; do
        test "$path" = "$check_dir" || fail "资产目录不允许嵌套目录：$path"
    done
    find "$check_dir" -type f -print | while IFS= read -r path; do
        name=${path#"$check_dir"/}
        valid_asset_name "$name" || fail "资产文件名或路径非法：$name"
        test "$name" = SHA256SUMS.txt || printf '%s\n' "$name" >> "$actual"
    done
    sort -u "$actual" -o "$actual"

    if ! cmp -s "$actual" "$expected"; then
        fail "产品资产集合与版本 ${asset_version} 不一致"
    fi

    while IFS= read -r line || test -n "$line"; do
        test -n "$line" || fail "SHA256SUMS.txt 含空行"
        hash=${line%% *}
        rest=${line#"$hash"}
        test "$rest" != "$line" || fail "SHA256SUMS.txt 格式非法：$line"
        case "$rest" in
            "  "*) name=${rest#??} ;;
            " "*)
                name=${rest#?}
                case "$name" in
                    \**) name=${name#\*} ;;
                    *) fail "SHA256SUMS.txt 文件名分隔格式非法：$line" ;;
                esac
                ;;
            *) fail "SHA256SUMS.txt 文件名分隔格式非法：$line" ;;
        esac
        valid_hash "$hash" || fail "SHA256SUMS.txt 含非法 SHA-256：$name"
        valid_asset_name "$name" || fail "SHA256SUMS.txt 含非法路径：$name"
        test "$name" != SHA256SUMS.txt || fail "SHA256SUMS.txt 不得校验自身"
        contains_line "$name" "$listed" && fail "SHA256SUMS.txt 含重复资产：$name"
        printf '%s\n' "$name" >> "$listed"
        path="$check_dir/$name"
        test -f "$path" || fail "SHA256SUMS.txt 引用了缺失资产：$name"
        actual_hash=$(file_hash "$path")
        test "$actual_hash" = "$hash" || fail "资产 SHA-256 不匹配：$name"
    done < "$sums"

    while IFS= read -r name; do
        contains_line "$name" "$listed" || fail "资产未列入 SHA256SUMS.txt：$name"
    done < "$actual"
    while IFS= read -r name; do
        contains_line "$name" "$actual" || fail "SHA256SUMS.txt 含多余资产：$name"
    done < "$listed"
    test -s "$actual" || fail "资产目录没有可校验产品资产"
    rm -rf "$work"
    trap - 0 HUP INT TERM
    printf '%s\n' "资产闭集校验通过：$check_dir"
}

real_directory_path() {
    directory=$1
    test -d "$directory" || fail "资产目录不存在：$directory"
    resolved=$(CDPATH= cd -P "$directory" 2>/dev/null && pwd -P) || fail "无法解析资产目录真实路径：$directory"
    printf '%s\n' "$resolved"
}

require_distinct_asset_directories() {
    rc_directory=$1
    ga_directory=$2
    rc_real=$(real_directory_path "$rc_directory")
    ga_real=$(real_directory_path "$ga_directory")
    test "$rc_real" != "$ga_real" || fail "RC 基准资产目录与 GA 资产目录必须指向不同的真实目录：$rc_real"
}

compare_asset_directories() {
    rc_assets_dir=$1
    ga_assets_dir=$2
    asset_version=$3
    names=$(expected_asset_names "$asset_version")
    names=$(printf '%s\n%s\n' "$names" SHA256SUMS.txt)
    for name in $names; do
        rc_path="$rc_assets_dir/$name"
        ga_path="$ga_assets_dir/$name"
        test -f "$rc_path" || fail "RC 基准资产缺失：$name"
        test -f "$ga_path" || fail "GA 资产缺失：$name"
        rc_size=$(file_size "$rc_path")
        ga_size=$(file_size "$ga_path")
        test "$rc_size" = "$ga_size" || fail "GA 资产大小与 RC 基准不一致：$name"
    done
    cmp -s "$rc_assets_dir/SHA256SUMS.txt" "$ga_assets_dir/SHA256SUMS.txt" || \
        fail "GA 资产 SHA-256 与 RC 基准不一致"
    printf '%s\n' "GA 资产与 RC 基准逐项一致：$ga_assets_dir"
}

commit_from_git() {
    tag=$1
    git rev-parse --verify "refs/tags/${tag}^{commit}" 2>/dev/null || fail "无法从真实 tag 解析 peeled commit：$tag"
}

verify_tag_identity() {
    label=$1
    tag=$2
    expected_commit=$3
    valid_sha "$expected_commit" || fail "$label commit 必须是 40 位小写 SHA"
    actual_commit=$(commit_from_git "$tag")
    test "$actual_commit" = "$expected_commit" || fail "$label tag 的 peeled commit 与给定身份不一致：$tag"
    printf '%s\n' "$actual_commit"
}

check_commits() {
    rc_commit=$1
    ga_commit=$2
    test "$rc_commit" = "$ga_commit" || fail "RC 与 GA 必须指向同一 commit"
}

audit_workflow() {
    workflow=$1
    test -f "$workflow" || fail "工作流文件不存在：$workflow"
    audit_error=$(awk '
        function reject(message) {
            print message
            failed = 1
            exit 1
        }
        {
            line = $0
            if (line == "  promote:" || line == "  ga:" || line == "  release:") {
                in_ga = 1
                found_ga = 1
                next
            }
            if (in_ga && line ~ /^  [^ ]+:/) {
                in_ga = 0
            }
            if (!in_ga) {
                next
            }
            if (index(line, "go build") || index(line, "make build") || index(line, "make agent") || index(line, "make package") || index(line, "make web") || (index(line, "gradlew") && (index(line, "build") || index(line, "assemble"))) || index(line, "npm run build") || (index(line, "pnpm") && index(line, "build")) || (index(line, "bun") && index(line, "build")) || index(line, "docker build") || index(line, "docker buildx build") || index(line, "docker/build-push-action") || index(line, "_build-release.yml") || index(line, "mvn package") || index(line, "mvn deploy")) {
                reject("GA job 不得包含构建或打包命令：" line)
            }
            if (index(line, "cosign sign") || index(line, "sign-blob") || index(line, "jarsigner") || index(line, "gh attestation sign") || (index(line, "gpg") && index(line, "--sign"))) {
                reject("GA job 不得包含产品重签命令：" line)
            }
        }
        END {
            if (failed) {
                exit 1
            }
            if (!found_ga) {
                reject("工作流缺少 GA job")
            }
        }
    ' "$workflow") || fail "$audit_error"
    printf '%s\n' "GA job 静态校验通过：$workflow"
}

version_file=VERSION
version_arg=
rc_tag=
ga_tag=
assets_dir=
rc_assets_dir=
rc_commit=
ga_commit=
workflow=

parse_common() {
    while test "$#" -gt 0; do
        case "$1" in
            --version-file) test "$#" -ge 2 || usage; version_file=$2; shift 2 ;;
            --version) test "$#" -ge 2 || usage; version_arg=$2; shift 2 ;;
            --rc-tag) test "$#" -ge 2 || usage; rc_tag=$2; shift 2 ;;
            --ga-tag) test "$#" -ge 2 || usage; ga_tag=$2; shift 2 ;;
            --assets-dir) test "$#" -ge 2 || usage; assets_dir=$2; shift 2 ;;
            --rc-assets-dir) test "$#" -ge 2 || usage; rc_assets_dir=$2; shift 2 ;;
            --rc-commit) test "$#" -ge 2 || usage; rc_commit=$2; shift 2 ;;
            --ga-commit) test "$#" -ge 2 || usage; ga_commit=$2; shift 2 ;;
            --workflow) test "$#" -ge 2 || usage; workflow=$2; shift 2 ;;
            *) fail "未知参数：$1" ;;
        esac
    done
}

check_identity() {
    version=$(read_version "$version_file")
    test -z "$version_arg" || test "$version_arg" = "$version" || fail "参数版本与 VERSION 不一致"
    version_arg=${version_arg:-$version}
    need_value --rc-tag "$rc_tag"
    need_value --ga-tag "$ga_tag"
    check_tags "$version" "$rc_tag" "$ga_tag"
    printf '%s\n' "版本与标签格式校验通过：$rc_tag -> $ga_tag"
}

command=${1:-}
test -n "$command" || usage
shift
case "$command" in
    check)
        parse_common "$@"
        check_identity
        test -z "$assets_dir" || fail "check 只做格式与工作流静态校验，不接受 --assets-dir"
        test -z "$rc_assets_dir" || fail "check 只做格式与工作流静态校验，不接受 --rc-assets-dir"
        test -z "$rc_commit" || fail "check 只做格式与工作流静态校验，不接受 --rc-commit"
        test -z "$ga_commit" || fail "check 只做格式与工作流静态校验，不接受 --ga-commit"
        test -z "$workflow" || audit_workflow "$workflow"
        ;;
    verify-rc)
        parse_common "$@"
        check_identity
        need_value --assets-dir "$assets_dir"
        need_value --rc-commit "$rc_commit"
        resolved_rc_commit=$(verify_tag_identity RC "$rc_tag" "$rc_commit")
        check_assets "$assets_dir" "$version_arg"
        printf '%s\n' "RC 校验通过：$rc_tag ($resolved_rc_commit)"
        ;;
    verify-ga)
        parse_common "$@"
        check_identity
        need_value --assets-dir "$assets_dir"
        need_value --rc-assets-dir "$rc_assets_dir"
        need_value --rc-commit "$rc_commit"
        need_value --ga-commit "$ga_commit"
        require_distinct_asset_directories "$rc_assets_dir" "$assets_dir"
        resolved_rc_commit=$(verify_tag_identity RC "$rc_tag" "$rc_commit")
        resolved_ga_commit=$(verify_tag_identity GA "$ga_tag" "$ga_commit")
        check_commits "$resolved_rc_commit" "$resolved_ga_commit"
        check_assets "$rc_assets_dir" "$version_arg"
        check_assets "$assets_dir" "$version_arg"
        compare_asset_directories "$rc_assets_dir" "$assets_dir" "$version_arg"
        printf '%s\n' "GA 校验通过：$ga_tag ($resolved_ga_commit)"
        ;;
    audit-ga-workflow)
        parse_common "$@"
        need_value --workflow "$workflow"
        audit_workflow "$workflow"
        ;;
    *) usage ;;
esac
