// 布局 Shell：侧栏 + 页眉 + 路由出口。
// 订阅 mock 场景切换（FR-159），场景变化时全量失效查询让所有页面重取数据。
import { useEffect } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'

import { subscribeMockScenario } from '@beacon/devmock'
import { useQueryClient } from '@tanstack/react-query'

import { ALL_PAGES } from '../routes'
import { useShellStore } from '../store'
import Header from './header'
import Sidebar from './sidebar'

export default function AppShell() {
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  const queryClient = useQueryClient()

  useEffect(() => {
    return subscribeMockScenario(() => {
      void queryClient.invalidateQueries()
    })
  }, [queryClient])

  return (
    <div className="app-bg min-h-screen text-foreground">
      <Sidebar />
      <main className={sidebarCollapsed ? undefined : 'md:pl-[232px]'}>
        <Header />
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
  )
}
