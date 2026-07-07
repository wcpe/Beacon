// 布局 Shell：侧栏 + 页眉 + 路由出口
import { Navigate, Route, Routes } from 'react-router-dom'

import { ALL_PAGES } from '../routes'
import { useShellStore } from '../store'
import Header from './header'
import Sidebar from './sidebar'

export default function AppShell() {
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)

  return (
    <div className="min-h-screen bg-background text-foreground">
      <Sidebar />
      <main className={sidebarCollapsed ? undefined : 'md:pl-56'}>
        <Header />
        <div className="grid gap-5 p-4">
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
