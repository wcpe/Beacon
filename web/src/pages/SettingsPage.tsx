// 设置聚合页骨架（FR-94，见 ADR-0043）：把原 `/settings` 升级为聚合页——
// 三块顶层 tab（运维设置 / 系统信息 / 系统设置），每块再含子 tab。
// 顶层三块用嵌套子路由（/settings/ops、/settings/system-info、/settings/system-config），
// 由 App.tsx 声明、本页用 <Outlet/> 承载；块内子 tab 用 search param（在各块组件内）。
// 一屏不滚动：本壳 h-full、顶层 tab 栏常驻不滚动，内容区交各块局部滚动。

import { useLocation, useNavigate, Outlet } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

// 三块顶层 tab → 子路由段。
const BLOCKS: Array<{ value: string; path: string; labelKey: string }> = [
  { value: 'ops', path: '/settings/ops', labelKey: 'settingsAggregate.blockOps' },
  {
    value: 'system-info',
    path: '/settings/system-info',
    labelKey: 'settingsAggregate.blockSystemInfo',
  },
  {
    value: 'system-config',
    path: '/settings/system-config',
    labelKey: 'settingsAggregate.blockSystemConfig',
  },
]

// 据当前 pathname 判定激活的顶层块（命中前缀；默认运维设置）。
function activeBlockOf(pathname: string): string {
  const hit = BLOCKS.find((b) => pathname === b.path || pathname.startsWith(`${b.path}/`))
  return hit?.value ?? 'ops'
}

export default function SettingsPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const location = useLocation()

  const activeBlock = activeBlockOf(location.pathname)
  // 切顶层块即跳对应子路由（保留 search param 由各块自管，这里只切块、不带子 tab）。
  const onBlockChange = (next: string) => {
    const target = BLOCKS.find((b) => b.value === next)
    if (target) navigate(target.path)
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-4">
      <div className="shrink-0">
        <h1 className="text-xl font-semibold">{t('settingsAggregate.title')}</h1>
      </div>

      {/* 顶层三块 tab 栏：常驻不滚动 */}
      <Tabs value={activeBlock} onValueChange={onBlockChange} className="shrink-0">
        <TabsList>
          {BLOCKS.map((block) => (
            <TabsTrigger key={block.value} value={block.value}>
              {t(block.labelKey)}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      {/* 块内容区：交由各块（含子 tab + 局部滚动）渲染 */}
      <div className="min-h-0 flex-1">
        <Outlet />
      </div>
    </div>
  )
}
