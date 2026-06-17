// 管理台路由：可深链的页面路由，默认重定向到 /configs。
// /login 在 Layout 外；其余页面经 RequireAuth 守卫（未登录跳登录）。
import { useEffect } from 'react'
import { Navigate, Route, Routes, useNavigate } from 'react-router-dom'
import Layout from './components/Layout'
import RequireAuth from './components/RequireAuth'
import LoginPage from './pages/LoginPage'
import ConfigsPage from './pages/ConfigsPage'
import FilesPage from './pages/FilesPage'
import OverrideSetsPage from './pages/OverrideSetsPage'
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
          {/* 默认进入配置中心 */}
          <Route index element={<Navigate to="/configs" replace />} />
          <Route path="configs" element={<ConfigsPage />} />
          {/* 配置详情走子路由，URL 可直接深链到具体配置项 */}
          <Route path="configs/:id" element={<ConfigsPage />} />
          {/* 文件树托管：列表与详情走子路由，可深链 */}
          <Route path="files" element={<FilesPage />} />
          <Route path="files/:id" element={<FilesPage />} />
          {/* 三方文件覆盖集：列表与详情走子路由，可深链 */}
          <Route path="override-sets" element={<OverrideSetsPage />} />
          <Route path="override-sets/:id" element={<OverrideSetsPage />} />
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
