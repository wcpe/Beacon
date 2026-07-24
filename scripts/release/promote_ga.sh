#!/bin/sh
# GA 晋级恢复单元：只复用同提交 tag 与既有资产，禁止重建、覆盖或补传。

set -eu

fail() {
    printf '%s\n' "GA 晋级失败：$*" >&2
    exit 1
}

need_env() {
    test -n "${2:-}" || fail "环境变量 $1 不能为空"
}

release_state() {
    metadata=$(gh release view "$GA_TAG" \
        --json tagName,isDraft,isPrerelease,publishedAt \
        --jq '.tagName + "|" + (.isPrerelease | tostring) + "|" + (if .isDraft then "draft" elif .publishedAt != null and .publishedAt != "" then "published" else "invalid" end)')
    tag_name=${metadata%%|*}
    release_metadata=${metadata#*|}
    prerelease=${release_metadata%%|*}
    state=${release_metadata#*|}
    test "$tag_name" = "$GA_TAG" || fail "GA Release tag 身份不一致：$GA_TAG"
    test "$prerelease" = false || fail "GA Release 不得为 prerelease：$GA_TAG"
    case "$state" in
        draft|published) printf '%s\n' "$state" ;;
        *) fail "无法识别 GA Release 状态：$GA_TAG" ;;
    esac
}

verify_ga() {
    make release-verify-ga \
        VERSION="$VERSION" \
        RELEASE_VERSION_FILE="$RELEASE_VERSION_FILE" \
        RELEASE_RC_TAG="$RC_TAG" \
        RELEASE_GA_TAG="$GA_TAG" \
        RELEASE_RC_ASSETS_DIR="$RC_ASSETS_DIR" \
        RELEASE_ASSETS_DIR="$1" \
        RELEASE_RC_COMMIT="$RC_COMMIT" \
        RELEASE_GA_COMMIT="$RC_COMMIT"
}

need_env RC_TAG "${RC_TAG:-}"
need_env GA_TAG "${GA_TAG:-}"
need_env RC_COMMIT "${RC_COMMIT:-}"
need_env VERSION "${VERSION:-}"
need_env GITHUB_REPOSITORY "${GITHUB_REPOSITORY:-}"

RELEASE_VERSION_FILE=${RELEASE_VERSION_FILE:-VERSION}
RC_ASSETS_DIR=${RELEASE_RC_ASSETS_DIR:-rc-assets}
GA_ASSETS_DIR=${RELEASE_GA_ASSETS_DIR:-ga-assets}
PUBLISHED_GA_ASSETS_DIR=${RELEASE_PUBLISHED_GA_ASSETS_DIR:-published-ga-assets}
GA_NOTES_FILE=${RELEASE_GA_NOTES_FILE:-ga-notes.md}

test -d "$RC_ASSETS_DIR" || fail "RC 资产目录不存在：$RC_ASSETS_DIR"
test ! -e "$GA_ASSETS_DIR" || fail "GA 资产目录必须为新目录：$GA_ASSETS_DIR"
test ! -e "$PUBLISHED_GA_ASSETS_DIR" || fail "公开 GA 资产目录必须为新目录：$PUBLISHED_GA_ASSETS_DIR"

ga_tag_exists=false
if git ls-remote --exit-code --tags origin "refs/tags/${GA_TAG}" >/dev/null 2>&1; then
    git fetch --force --no-tags origin "+refs/tags/${GA_TAG}:refs/tags/${GA_TAG}"
    ga_commit=$(git rev-parse "refs/tags/${GA_TAG}^{commit}")
    test "$ga_commit" = "$RC_COMMIT" || fail "已有 GA tag 的 peeled commit 与 RC commit 不一致：$GA_TAG"
    ga_tag_exists=true
fi

ga_release_state=missing
if gh release view "$GA_TAG" >/dev/null 2>&1; then
    ga_release_state=$(release_state)
fi
if test "$ga_release_state" != missing && test "$ga_tag_exists" != true; then
    fail "GA Release 已存在但真实 GA tag 缺失：$GA_TAG"
fi

if test "$ga_tag_exists" = false; then
    gh api --method POST "repos/${GITHUB_REPOSITORY}/git/refs" \
        -f "ref=refs/tags/${GA_TAG}" \
        -f "sha=${RC_COMMIT}" >/dev/null
elif test "$ga_tag_exists" != true; then
    fail "无法识别 GA tag 状态：$ga_tag_exists"
fi

git fetch --force --no-tags origin "+refs/tags/${GA_TAG}:refs/tags/${GA_TAG}"
ga_commit=$(git rev-parse "refs/tags/${GA_TAG}^{commit}")
test "$ga_commit" = "$RC_COMMIT" || fail "GA tag 的 peeled commit 与 RC commit 不一致：$GA_TAG"

mkdir "$GA_ASSETS_DIR"
case "$ga_release_state" in
    missing) cp "$RC_ASSETS_DIR"/* "$GA_ASSETS_DIR"/ ;;
    draft|published) gh release download "$GA_TAG" --dir "$GA_ASSETS_DIR" ;;
    *) fail "无法识别 GA Release 状态：$ga_release_state" ;;
esac
verify_ga "$GA_ASSETS_DIR"

if test "$ga_release_state" = missing; then
    {
        printf 'Beacon %s 正式版。\n\n' "$VERSION"
        printf '由同提交 RC 原样晋级，产品资产及 SHA256SUMS.txt 保持不变。\n'
    } > "$GA_NOTES_FILE"
    gh release create "$GA_TAG" "$GA_ASSETS_DIR"/* \
        --draft \
        --title "$GA_TAG" \
        --notes-file "$GA_NOTES_FILE" \
        --verify-tag
fi

current_state=$(release_state)
if test "$current_state" = draft; then
    gh release edit "$GA_TAG" --draft=false --prerelease=false --latest
elif test "$current_state" != published; then
    fail "无法识别 GA Release 草稿状态：$GA_TAG"
fi
test "$(release_state)" = published || fail "GA Release 未成功公开：$GA_TAG"

mkdir "$PUBLISHED_GA_ASSETS_DIR"
gh release download "$GA_TAG" --dir "$PUBLISHED_GA_ASSETS_DIR"
verify_ga "$PUBLISHED_GA_ASSETS_DIR"
