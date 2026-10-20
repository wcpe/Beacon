// 交付域各页统一的「文档」入口：页头右侧操作区里的一个描边小按钮，点击在新窗口打开对应章节。
//
// 只做「跳到文档」一件事：按钮文字走 i18n（delivery.help.docButton），
// 页面功能的解释由 PageHeader 的 description 承担，这里不再叠一层悬停提示。
//
// 锚点约定：文档正文的目标标题都加了显式英文 id（如 `## 3. 文件资产与受管文件 {#assets}`），
// 因此这里只拼英文锚点，标题措辞以后再改也不会把这些链接改断。
import { ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'

// 使用者文档站（Docusaurus，路由前缀 /docs；只有 wiki 等使用者文档进站）
const DOCS_WIKI = 'https://beacon.wcpe.top/docs/wiki'

// 页面 → 文档章节
const DOC_LINKS = {
  approvals: `${DOCS_WIKI}/approval-lifecycle-and-mcp#approvals`,
  assets: `${DOCS_WIKI}/configuration-and-delivery#assets`,
  configs: `${DOCS_WIKI}/configuration-and-delivery#configs`,
  changes: `${DOCS_WIKI}/configuration-and-delivery#changes`,
  history: `${DOCS_WIKI}/configuration-and-delivery#history`,
} as const

export type DocHelpTopic = keyof typeof DOC_LINKS

export default function DocHelp({ topic }: { topic: DocHelpTopic }) {
  const { t } = useTranslation()
  return (
    <Button asChild variant="outline" size="sm">
      {/* 链接即按钮本体：键盘 Enter 可直接打开，外链图标提示会开新窗口 */}
      <a href={DOC_LINKS[topic]} target="_blank" rel="noreferrer">
        {t('delivery.help.docButton')}
        <ExternalLink className="size-3.5" aria-hidden />
      </a>
    </Button>
  )
}
