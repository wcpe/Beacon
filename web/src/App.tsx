// 管理台路由：可深链的页面路由，默认重定向到 /configs。
// /login 在 Layout 外；其余页面经 RequireAuth 守卫（未登录跳登录）。
// 配置 / 文件的详情改为独立路由页（/configs/:id、/files/:id）；覆盖集详情仍由列表页内开 Sheet。
import { useEffect } from 'react'
import { Navigate, Route, Routes, useNavigate } from 'react-router-dom'
import Layout from './components/Layout'
import RequireAuth from './components/RequireAuth'
import LoginPage from './pages/LoginPage'
import DashboardPage from './pages/DashboardPage'
import ConfigsPage from './pages/ConfigsPage'
import InstancesPage from './pages/InstancesPage'
import ZonesPage from './pages/ZonesPage'
import AuditsPage from './pages/AuditsPage'
import NamespacesPage from './pages/NamespacesPage'
import { setOnUnauthorized } from './api/client'

export default function App() {
  const navigate = useNavigate()
  // 注册 401 全局处理：令牌失效时跳登录（client 已先清登录态）
  useEffect(() => {
    setOnUnauthorized(() => navigate('/login', { replace: true }))
  }, [navigate])

  return (
    <Routes>
      {/* 登录页：无侧栏布局、无需令牌 */}
      <Route path="/login" element={<LoginPage />} />
      {/* 受保护区：未登录跳登录 */}
      <Route element={<RequireAuth />}>
        <Route path="/" element={<Layout />}>
          {/* 默认进入配置中心（单页面：列表 + 详情 + Diff + 历史） */}
          <Route index element={<Navigate to="/configs" replace />} />
          {/* 可观测看板（FR-32）：总览卡片 + 趋势图 + 每服明细 */}
          <Route path="dashboard" element={<DashboardPage />} />
          <Route path="configs" element={<ConfigsPage />} />
          {/* 旧链接 /configs/:id 重定向到单页面 */}
          <Route path="configs/:id" element={<Navigate to="/configs" replace />} />
          {/* 文件树托管 / 覆盖集已下线，重定向到配置中心 */}
          <Route path="files" element={<Navigate to="/configs" replace />} />
          <Route path="files/:id" element={<Navigate to="/configs" replace />} />
          <Route path="override-sets" element={<Navigate to="/configs" replace />} />
          <Route path="override-sets/:id" element={<Navigate to="/configs" replace />} />
          <Route path="instances" element={<InstancesPage />} />
          <Route path="zones" element={<ZonesPage />} />
          <Route path="audits" element={<AuditsPage />} />
          <Route path="namespaces" element={<NamespacesPage />} />
          {/* 未知路径回到配置中心 */}
          <Route path="*" element={<Navigate to="/configs" replace />} />
        </Route>
      </Route>
    </Routes>
  )
}
