// 管理台路由：可深链的页面路由，默认重定向到 /configs。
// /login 在 Layout 外；其余页面经 RequireAuth 守卫（未登录跳登录）。
// 配置 / 文件的详情改为独立路由页（/configs/:id、/files/:id）；覆盖集详情仍由列表页内开 Sheet。
import { useEffect } from 'react'
import { Navigate, Route, Routes, useNavigate } from 'react-router-dom'
import Layout from './components/Layout'
import RequireAuth from './components/RequireAuth'
import WallboardLayout from './components/WallboardLayout'
import LoginPage from './pages/LoginPage'
import DashboardPage from './pages/DashboardPage'
import WallboardPage from './pages/WallboardPage'
import ConfigsPage from './pages/ConfigsPage'
import FilePreviewPage from './pages/FilePreviewPage'
import ImprintPage from './pages/ImprintPage'
import ReverseFetchTaskPage from './pages/reverse-fetch/ReverseFetchTaskPage'
import ServersPage from './pages/ServersPage'
import TopologyPage from './pages/TopologyPage'
import ZonesPage from './pages/ZonesPage'
import AuditsPage from './pages/AuditsPage'
import AlertEventsPage from './pages/AlertEventsPage'
import ServiceAnalysisPage from './pages/ServiceAnalysisPage'
import ApiKeysPage from './pages/ApiKeysPage'
import NamespacesPage from './pages/NamespacesPage'
import SettingsPage from './pages/SettingsPage'
import OpsSettingsBlock from './pages/settings/OpsSettingsBlock'
import SystemInfoBlock from './pages/settings/SystemInfoBlock'
import SystemConfigBlock from './pages/settings/SystemConfigBlock'
import SystemObservabilityPage from './pages/SystemObservabilityPage'
import { setOnUnauthorized } from './api/client'
import { applyThemeToDocument, usePreferences } from './state/preferences'

export default function App() {
  const navigate = useNavigate()
  // 主题偏好（FR-92）：订阅偏好 store，运行期切换时同步 .dark 类（首屏已在 main.tsx 同步应用过）。
  const { theme } = usePreferences()
  useEffect(() => {
    applyThemeToDocument(theme)
  }, [theme])
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
        {/* NOC 大屏只读看板（FR-92）：无侧栏极简布局，纯只读不含任何操作入口 */}
        <Route path="/wallboard" element={<WallboardLayout />}>
          <Route index element={<WallboardPage />} />
        </Route>
        <Route path="/" element={<Layout />}>
          {/* 默认进入配置中心（单页面：列表 + 详情 + Diff + 历史） */}
          <Route index element={<Navigate to="/configs" replace />} />
          {/* 可观测看板（FR-32）：总览卡片 + 趋势图 + 每服明细 */}
          <Route path="dashboard" element={<DashboardPage />} />
          <Route path="configs" element={<ConfigsPage />} />
          {/* 旧链接 /configs/:id 重定向到单页面 */}
          <Route path="configs/:id" element={<Navigate to="/configs" replace />} />
          {/* 文件树有效预览（FR-45）：只读预览某服合并后文件树 + 逐键来源 */}
          <Route path="file-preview" element={<FilePreviewPage />} />
          {/* 拓印审核台（FR-46）：选在线服+文件 → diff → 单人自审 → 同步 */}
          <Route path="imprint" element={<ImprintPage />} />
          {/* 反向抓取审核台 + 任务台（FR-60）：建扫描任务 → 审核清单 → 提交 → 冲突 diff → resolve */}
          <Route path="reverse-fetch" element={<ReverseFetchTaskPage />} />
          {/* 文件树托管 / 覆盖集已下线，重定向到配置中心 */}
          <Route path="files" element={<Navigate to="/configs" replace />} />
          <Route path="files/:id" element={<Navigate to="/configs" replace />} />
          <Route path="override-sets" element={<Navigate to="/configs" replace />} />
          <Route path="override-sets/:id" element={<Navigate to="/configs" replace />} />
          {/* 服务器页（FR-65）：实例与健康 + 代理服管理合并，列全部 bukkit+bungee + 深指标 + 下线/drain/改派 */}
          <Route path="servers" element={<ServersPage />} />
          {/* 旧 /instances、/proxies 重定向到统一服务器页（FR-65） */}
          <Route path="instances" element={<Navigate to="/servers" replace />} />
          <Route path="proxies" element={<Navigate to="/servers" replace />} />
          {/* 集群拓扑可视化（FR-37）：bc→bukkit 真实连线图 */}
          <Route path="topology" element={<TopologyPage />} />
          <Route path="zones" element={<ZonesPage />} />
          <Route path="audits" element={<AuditsPage />} />
          {/* 告警事件信息流页（FR-89）：系统健康事件历史留痕时间线 + 类型/级别/环境/时间过滤 */}
          <Route path="alert-events" element={<AlertEventsPage />} />
          {/* 服务分析页（FR-73）：按时间窗 + 环境聚合运维操作审计（KPI + 动作分布 + 每日趋势） */}
          <Route path="service-analysis" element={<ServiceAnalysisPage />} />
          {/* 密钥管理（FR-42）：只读角色 + 运行时 API 密钥创建/吊销/重置 */}
          <Route path="api-keys" element={<ApiKeysPage />} />
          <Route path="namespaces" element={<NamespacesPage />} />
          {/* 设置聚合页骨架（FR-94，见 ADR-0043）：三块顶层 tab + 块内子 tab。
              顶层三块用嵌套子路由，/settings 重定向到运维设置块；块内子 tab 用 search param。 */}
          <Route path="settings" element={<SettingsPage />}>
            <Route index element={<Navigate to="/settings/ops" replace />} />
            {/* 运维设置块（FR-62/FR-77）：6 域子 tab + 跨子 tab 统一草稿 / 批量保存 / 恢复默认 */}
            <Route path="ops" element={<OpsSettingsBlock />} />
            {/* 系统信息块（FR-94 骨架）：版本与更新 / 控制面健康 空壳子 tab */}
            <Route path="system-info" element={<SystemInfoBlock />} />
            {/* 系统设置块（FR-94 骨架）：网络代理 / 更新设置 / API 密钥 / 环境管理 空壳子 tab */}
            <Route path="system-config" element={<SystemConfigBlock />} />
          </Route>
          {/* 控制面健康页（FR-82）：控制面自身内部运行态只读自观测（DB 连接池 / 长轮询挂起 / 注册表规模 / 命令队列深度） */}
          <Route path="system" element={<SystemObservabilityPage />} />
          {/* 未知路径回到配置中心 */}
          <Route path="*" element={<Navigate to="/configs" replace />} />
        </Route>
      </Route>
    </Routes>
  )
}
