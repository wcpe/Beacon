import React, { useEffect } from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter, Route, Routes, useNavigate } from 'react-router-dom'
import { Toaster } from '@beacon/ui'
import '@beacon/ui/styles.css'
import './styles.css'
import './i18n'
import AppShell from './shell/app-shell'
import DocumentTitle from './shell/document-title'
import RequireAuth from './shell/require-auth'
import LoginPage from './pages/login'
import { isDemoMode } from './demo-mode'
import { setOnUnauthorized } from './state/auth'

const queryClient = new QueryClient()

// 应用路由（须在 BrowserRouter 内以取用 useNavigate）：登录页公开，其余路径经路由守卫。
function AppRoutes() {
  const navigate = useNavigate()
  useEffect(() => {
    // 注册全局 401 回调（FR-179）：任意 /admin/* 遇 401 时请求层已清令牌，这里负责跳登录。
    // 放应用层持有 router，避免 api 反向依赖 router。
    setOnUnauthorized(() => {
      navigate('/login', { replace: true })
    })
  }, [navigate])

  return (
    <>
      {/* 须在 BrowserRouter 内：随路由切换 document.title = "Beacon - 当前页面" */}
      <DocumentTitle />
      <Routes>
        {/* 登录页公开、全屏、独立于 AppShell（无侧栏无页眉） */}
        <Route element={<LoginPage />} path="/login" />
        {/* 其余路径进管理台 Shell，经路由守卫：无令牌且非 demo → 跳登录；demo 免登录放行 */}
        <Route
          element={
            <RequireAuth>
              <AppShell />
            </RequireAuth>
          }
          path="/*"
        />
      </Routes>
    </>
  )
}

async function bootstrap() {
  // 仅演示模式注册 mock worker：常规发布产物不得拦截 /admin/*，否则真实控制面不可达。
  // 动态 import 使 @beacon/devmock 的 handlers 不进入生产包。
  if (isDemoMode()) {
    const { startControlPlaneMocking } = await import('@beacon/devmock')
    await startControlPlaneMocking()
  }
  const rootElement = document.getElementById('root')
  if (!rootElement) {
    throw new Error('缺少应用根节点')
  }

  ReactDOM.createRoot(rootElement).render(
    <React.StrictMode>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <AppRoutes />
        </BrowserRouter>
        {/* 写操作成功/失败 toast 挂载点；勿用页内横幅挤布局 */}
        <Toaster position="top-center" richColors closeButton />
      </QueryClientProvider>
    </React.StrictMode>,
  )
}

void bootstrap()
