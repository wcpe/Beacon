// 页面级标题地标（sr-only 的 <h1>）：给每个路由页补上「本页叫什么」这一条顶部标题节点。
//
// 为什么需要它：页眉不再承载面包屑后，页面身份改由内容区表达，但只有接入 PageHeader 的页面
// 才真正输出 <h1>。其余页面若直接铺 KPI 卡 / 列表，文档大纲就没有页面节点，读屏用户也失去
// 「跳到页面标题」的锚点——这是无障碍回归，本组件负责补位。
//
// 用法：作为页面根元素的第一个子节点，titleKey 一律取 routes.tsx 的 NavPage.titleKey（nav.* 域），
// 与侧栏标签、浏览器标签页标题同源，不新造文案：
//   <section className="grid gap-4">
//     <PageTitle titleKey="nav.audits" />
//     ...
//   </section>
//
// 两处取舍：
// 1) 用 sr-only 而非可见标题：这些页面的视觉层级已由页内卡片与工具条确定，补一个可见大标题会
//    改变布局与信息密度；隐藏标题只补语义、不动像素。
// 2) 只隐藏不降级为 h2：一页只应有一个 h1。如果某页确实需要可见大标题，请改用 @beacon/ui 的
//    PageHeader（它内部已是 h1），并同时移除本组件，避免一页出现两个 h1。
import { useTranslation } from 'react-i18next'

interface PageTitleProps {
  // 页面标题的 i18n 键（nav.* 域，与侧栏、document.title 共用）
  titleKey: string
}

export default function PageTitle({ titleKey }: PageTitleProps) {
  const { t } = useTranslation()
  return <h1 className="sr-only">{t(titleKey)}</h1>
}
