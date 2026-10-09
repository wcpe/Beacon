/**
 * Remark 插件：把使用者文档里指向**内部文档目录**的相对链接重写为仓库绝对链接。
 *
 * 背景：站点通过 `include` 白名单只收录面向使用者的文档（wiki / OPERATIONS / SDK），
 * 内部文档（adr / specs / PRD / ROADMAP/ API 等）不进站。若使用者文档中残留指向这些
 * 目录的相对链接，站点构建会报断链（`onBrokenLinks: 'throw'`）。
 *
 * 现状：使用者文档已做过「零内部引用」清理，本插件是**护栏**——
 * 即便将来误加内部链接，也不会让站点构建失败或暴露内部路径：
 * 链接被重写为 GitHub 上的仓库绝对地址，读者点开看到的是源码仓库中的原文。
 *
 * 用法（docusaurus.config.ts）：作为 remarkPlugins 传入，无需参数。
 */

/** 需要重写为仓库绝对链接的内部目录前缀（相对 docs/）。 */
const INTERNAL_PREFIXES = ['adr/', 'specs/', 'ADR/', 'SPECS/']

/** 仓库内文档的绝对地址前缀。 */
const REPO_BLOB = 'https://github.com/wcpe/Beacon/blob/master/docs/'

/**
 * 判断链接目标是否指向内部文档目录。
 * 排除外链（http/https/mailto）与纯锚点（#开头）。
 */
function isInternalDocLink(url: string): boolean {
  if (!url) {
    return false
  }
  if (/^[a-z][a-z0-9+.-]*:/i.test(url) || url.startsWith('#') || url.startsWith('//')) {
    return false
  }
  // 归一化：去掉开头的 ./ 与 ../（使用者文档位于 docs/wiki/，内部目录在其上一级）
  const normalized = url.replace(/^(\.\.?\/)+/, '').replace(/^\/+/, '')
  return INTERNAL_PREFIXES.some((prefix) => normalized.startsWith(prefix))
}

/** 把相对链接重写为仓库绝对链接（保留原有锚点）。 */
function toRepoUrl(url: string): string {
  const normalized = url.replace(/^(\.\.?\/)+/, '').replace(/^\/+/, '')
  return REPO_BLOB + normalized
}

/** 深度遍历 mdast 节点，重写 link / definition 两类链接节点的 url。 */
function rewriteLinks(node: unknown): void {
  if (!node || typeof node !== 'object') {
    return
  }
  const current = node as {type?: string; url?: string; children?: unknown[]}

  if (
    (current.type === 'link' || current.type === 'definition') &&
    typeof current.url === 'string' &&
    isInternalDocLink(current.url)
  ) {
    current.url = toRepoUrl(current.url)
  }

  if (Array.isArray(current.children)) {
    for (const child of current.children) {
      rewriteLinks(child)
    }
  }
}

/** remark 插件入口。 */
export default function remarkInternalDocLinks() {
  return (tree: unknown): void => {
    rewriteLinks(tree)
  }
}
