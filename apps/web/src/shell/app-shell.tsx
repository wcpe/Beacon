// 布局 Shell：固定侧栏 + 固定页眉 + 独立滚动的内容区 + 路由出口。
// 页眉不随内容滚动；滚动只发生在内容区（overflow-y-auto），滚动条用 scrollbar-gutter:stable
// 预留占位——出现/消失都占位，避免布局横向跳动。
// 侧栏折叠触发器为浮于侧栏右缘、垂直居中的圆形按钮（跨侧栏/内容交界）。
// 订阅 mock 场景切换（FR-159），场景变化时全量失效查询让所有页面重取数据。
import { useEffect } from 'react'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

import { subscribeMockScenario } from '@beacon/devmock'
import { useQueryClient } from '@tanstack/react-query'

import { ALL_PAGES } from '../routes'
import { useShellStore } from '../store'
import Header from './header'
import Sidebar from './sidebar'

export default function AppShell() {
  const { t } = useTranslation()
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  const toggleSidebar = useShellStore((state) => state.toggleSidebar)
  const queryClient = useQueryClient()
  const toggleLabel = sidebarCollapsed ? t('common.sidebar.expand') : t('common.sidebar.collapse')

  useEffect(() => {
    return subscribeMockScenario(() => {
      void queryClient.invalidateQueries()
    })
  }, [queryClient])

  return (
    <div className="app-bg h-screen overflow-hidden text-foreground">
      <Sidebar />

      {/* 折叠触发器：浮于侧栏右缘、垂直居中的小圆按钮，跨侧栏与内容交界 */}
      <button
        aria-label={toggleLabel}
        title={toggleLabel}
        type="button"
        onClick={toggleSidebar}
        className={[
          'fixed top-1/2 z-30 hidden size-6 -translate-y-1/2 place-items-center rounded-full',
          'border border-border bg-card text-ink-3 shadow-card transition',
          'hover:border-border-strong hover:text-ink-1 md:grid',
          sidebarCollapsed ? 'left-2' : 'left-[220px]',
        ].join(' ')}
      >
        {sidebarCollapsed ? (
          <ChevronRight className="size-3.5" />
        ) : (
          <ChevronLeft className="size-3.5" />
        )}
      </button>

      <div
        className={[
          'flex h-screen flex-col',
          sidebarCollapsed ? '' : 'md:pl-[232px]',
        ].join(' ')}
      >
        <Header />
        <main className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
          <div className="grid gap-5 p-4 sm:px-[22px] sm:py-[18px]">
            <Routes>
              <Route element={<Navigate replace to="/dashboard" />} path="/" />
              {ALL_PAGES.map((page) => (
                <Route element={<page.Component />} key={page.path} path={page.path} />
              ))}
            </Routes>
          </div>
        </main>
      </div>
    </div>
  )
}
