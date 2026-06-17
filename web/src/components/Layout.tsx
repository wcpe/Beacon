// 顶层布局：侧边导航 + 当前登录身份 + 登出 + 主内容区。
// 操作者身份由登录令牌决定（FR-11），写操作 operator 以认证身份为准，无需手填。

import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { clearAuth, useAuth } from '../state/auth'

// 导航项定义（路径 + 中文名）
const NAV_ITEMS: Array<{ to: string; label: string }> = [
  { to: '/configs', label: '配置中心' },
  { to: '/files', label: '文件树托管' },
  { to: '/override-sets', label: '文件覆盖集' },
  { to: '/instances', label: '实例与健康' },
  { to: '/zones', label: 'zone 分配' },
  { to: '/audits', label: '审计日志' },
  { to: '/namespaces', label: '环境管理' },
]

export default function Layout() {
  const { operator } = useAuth()
  const navigate = useNavigate()

  function onLogout() {
    clearAuth()
    navigate('/login', { replace: true })
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">Beacon 管理台</div>
        <nav className="nav">
          {NAV_ITEMS.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) => (isActive ? 'nav-link active' : 'nav-link')}
            >
              {item.label}
            </NavLink>
          ))}
        </nav>
        <div className="operator-box">
          <div className="operator-label">当前操作人</div>
          <div className="operator-name">{operator || '-'}</div>
          <button type="button" className="logout-btn" onClick={onLogout}>
            登出
          </button>
        </div>
      </aside>
      <main className="content">
        <Outlet />
      </main>
    </div>
  )
}
