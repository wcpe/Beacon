// 页眉面包屑（E3）：按当前路由在导航 IA（routes.tsx）解析「分组 › 页面」，紧随环境 / 命名空间
// 选择器展示当前页身份。E5 后内容区不再渲染大标题，页面身份由「选择器 + 本面包屑」共同承担，
// 故此处不重复渲染环境名（已由 EnvFilter 展示）。复用 nav i18n 键，不新增业务文案。
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'

import { APPROVALS_PAGE, DASHBOARD_PAGE, NAV_GROUPS } from '../routes'

interface Crumb {
  // 所属大域分组 i18n 键（nav.groups.*）；顶层页（运维总览 / 全局审批）无分组
  groupKey?: string
  // 页面标题 i18n 键（nav.*）
  titleKey: string
  path: string
}

// 导航 IA 之外仍有页面的独立路由（如 /license 仅底栏入口、不进 ALL_PAGES），仍需在面包屑给出身份。
const EXTRA_ROUTES: Crumb[] = [{ titleKey: 'common.license.pageTitle', path: '/license' }]

// 扁平化导航 IA：顶层页 + 四大域分组页 + IA 外独立页
function routeTable(): Crumb[] {
  const rows: Crumb[] = [
    { titleKey: DASHBOARD_PAGE.titleKey, path: DASHBOARD_PAGE.path },
    { titleKey: APPROVALS_PAGE.titleKey, path: APPROVALS_PAGE.path },
    ...EXTRA_ROUTES,
  ]
  for (const group of NAV_GROUPS) {
    for (const page of group.pages) {
      rows.push({ groupKey: group.titleKey, titleKey: page.titleKey, path: page.path })
    }
  }
  return rows
}

// 最长前缀匹配：/changes/history 必须命中 /changes/history，而非其父 /changes。
function matchCrumb(pathname: string): Crumb | null {
  let best: Crumb | null = null
  for (const row of routeTable()) {
    const hit = pathname === row.path || pathname.startsWith(`${row.path}/`)
    if (hit && (best === null || row.path.length > best.path.length)) {
      best = row
    }
  }
  return best
}

export default function Breadcrumb() {
  const { t } = useTranslation()
  const { pathname } = useLocation()
  const crumb = matchCrumb(pathname)
  if (crumb === null) {
    return null
  }
  return (
    <nav
      data-slot="page-breadcrumb"
      aria-label={t('common.breadcrumb')}
      className="flex min-w-0 items-center gap-1.5"
    >
      {crumb.groupKey != null && (
        <>
          <span className="shrink-0 text-[12.5px] text-ink-4">{t(crumb.groupKey)}</span>
          <ChevronRight className="size-3 shrink-0 text-ink-4" aria-hidden />
        </>
      )}
      <span className="truncate text-[12.5px] font-medium text-ink-1">{t(crumb.titleKey)}</span>
    </nav>
  )
}
