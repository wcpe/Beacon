/**
 * 汉化本地搜索框的占位文案。
 *
 * 为什么用客户端模块而非 swizzle：`docusaurus-lunr-search` 把 placeholder 硬编码在
 * 其 SearchBar 组件里（`Search Ctrl+K` / `Search ⌘+K`），未走 Docusaurus 的 i18n 文案表。
 * 为一个字符串 swizzle 整个组件会锁定该文件、插件升级时需手工同步；此处只在运行时
 * 改写该属性，代价小且不侵入插件代码。
 *
 * 若将来插件提供了文案配置项，可移除本模块。
 */

const PLACEHOLDER_PATTERN = /^Search\s*(⌘\+K|Ctrl\+K)$/

/** 把英文占位文案替换为中文，保留原有的快捷键提示。 */
function localize(input: HTMLInputElement): void {
  const match = input.placeholder.match(PLACEHOLDER_PATTERN)
  if (match) {
    input.placeholder = `搜索文档 ${match[1]}`
  }
}

/** 站点为客户端渲染，需在挂载后与后续 DOM 变更时都应用一次。 */
export function onRouteDidUpdate(): void {
  if (typeof document === 'undefined') {
    return
  }

  const apply = (): void => {
    document
      .querySelectorAll<HTMLInputElement>('input.navbar__search-input')
      .forEach(localize)
  }

  apply()

  // 搜索框在索引就绪后会重设 placeholder（Loading… → 快捷键提示），故持续观察。
  const observer = new MutationObserver(apply)
  observer.observe(document.body, {
    childList: true,
    subtree: true,
    attributeFilter: ['placeholder'],
  })
}
