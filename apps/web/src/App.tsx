import { useQuery } from '@tanstack/react-query'
import { Activity, Boxes, PanelLeftClose, PanelLeftOpen, Settings } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { NavLink, Route, Routes } from 'react-router-dom'

import {
  Badge,
  Button,
  DataTable,
  SectionHeader,
  SummaryStrip,
  type DataTableColumn,
  type SummaryItem,
} from '@beacon/ui'

import { fetchControlPlaneStatus, type ControlPlaneStatus } from './query'
import { useShellStore, type AppView } from './store'

interface RouteItem {
  to: string
  label: string
  icon: typeof Activity
  view: AppView
}

const routes: RouteItem[] = [
  { to: '/', label: 'nav.overview', icon: Activity, view: 'overview' },
  { to: '/topology', label: 'nav.topology', icon: Boxes, view: 'topology' },
  { to: '/settings', label: 'nav.settings', icon: Settings, view: 'settings' },
]

const columns: DataTableColumn<ControlPlaneStatus>[] = [
  { header: '阶段', cell: (row) => row.phase },
  { header: '版本线', cell: (row) => row.release },
  { header: '入口', cell: (row) => row.web },
]

function AppShell() {
  const { t } = useTranslation()
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  const setActiveView = useShellStore((state) => state.setActiveView)
  const toggleSidebar = useShellStore((state) => state.toggleSidebar)
  const status = useQuery({
    queryKey: ['control-plane-status'],
    queryFn: fetchControlPlaneStatus,
  })
  const items: SummaryItem[] = [
    { label: '状态', value: t('status.ready'), tone: 'success' },
    { label: '数据', value: t('status.mock'), tone: 'warning' },
    { label: 'Legacy', value: t('status.legacy'), tone: 'muted' },
  ]

  return (
    <div className="min-h-screen bg-background text-foreground">
      <aside
        className={[
          'fixed inset-y-0 left-0 hidden w-56 border-r bg-card px-3 py-4',
          sidebarCollapsed ? 'md:hidden' : 'md:block',
        ].join(' ')}
      >
        <div className="mb-5 flex items-center gap-2 px-2">
          <div className="flex size-8 items-center justify-center rounded-md bg-primary text-primary-foreground">
            B
          </div>
          <div>
            <div className="text-sm font-semibold">Beacon</div>
            <div className="text-xs text-muted-foreground">0.21.x</div>
          </div>
        </div>
        <nav className="grid gap-1">
          {routes.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === '/'}
              onClick={() => { setActiveView(item.view); }}
              className={({ isActive }) =>
                [
                  'flex items-center gap-2 rounded-md px-2 py-2 text-sm transition-colors',
                  isActive ? 'bg-accent text-accent-foreground' : 'text-muted-foreground hover:bg-muted',
                ].join(' ')
              }
            >
              <item.icon className="size-4" />
              <span>{t(item.label)}</span>
            </NavLink>
          ))}
        </nav>
      </aside>

      <main className={sidebarCollapsed ? undefined : 'md:pl-56'}>
        <header className="flex min-h-12 items-center justify-between border-b bg-background px-4">
          <div className="flex min-w-0 items-center gap-2">
            <Button
              aria-label={sidebarCollapsed ? '展开导航' : '收起导航'}
              size="icon"
              title={sidebarCollapsed ? '展开导航' : '收起导航'}
              variant="ghost"
              onClick={toggleSidebar}
            >
              {sidebarCollapsed ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
            </Button>
            <Badge variant="secondary">apps/web</Badge>
            <span className="truncate text-sm text-muted-foreground">第二版管理台</span>
          </div>
          <Button size="sm" variant="outline">
            {t('status.ready')}
          </Button>
        </header>

        <div className="grid gap-5 p-4">
          <SummaryStrip items={items} />
          <Routes>
            <Route
              path="/"
              element={
                <section className="grid gap-3">
                  <SectionHeader title="工程化总览" count="第二版入口" />
                  <DataTable
                    columns={columns}
                    rows={status.data ? [status.data] : []}
                    emptyText="暂无状态"
                    rowKey={(row) => row.web}
                  />
                </section>
              }
            />
            <Route path="/topology" element={<Placeholder title="拓扑" />} />
            <Route path="/settings" element={<Placeholder title="设置" />} />
          </Routes>
        </div>
      </main>
    </div>
  )
}

function Placeholder({ title }: { title: string }) {
  return (
    <section className="grid gap-3">
      <SectionHeader title={title} />
      <div className="rounded-md border bg-card p-4 text-sm text-muted-foreground">暂无数据</div>
    </section>
  )
}

export default AppShell
