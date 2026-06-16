// 管理台路由：可深链的页面路由，默认重定向到 /configs。
import { Navigate, Route, Routes } from 'react-router-dom'
import Layout from './components/Layout'
import ConfigsPage from './pages/ConfigsPage'
import InstancesPage from './pages/InstancesPage'
import ZonesPage from './pages/ZonesPage'
import AuditsPage from './pages/AuditsPage'
import NamespacesPage from './pages/NamespacesPage'

export default function App() {
  return (
    <Routes>
      <Route path="/" element={<Layout />}>
        {/* 默认进入配置中心 */}
        <Route index element={<Navigate to="/configs" replace />} />
        <Route path="configs" element={<ConfigsPage />} />
        {/* 配置详情走子路由，URL 可直接深链到具体配置项 */}
        <Route path="configs/:id" element={<ConfigsPage />} />
        <Route path="instances" element={<InstancesPage />} />
        <Route path="zones" element={<ZonesPage />} />
        <Route path="audits" element={<AuditsPage />} />
        <Route path="namespaces" element={<NamespacesPage />} />
        {/* 未知路径回到配置中心 */}
        <Route path="*" element={<Navigate to="/configs" replace />} />
      </Route>
    </Routes>
  )
}
