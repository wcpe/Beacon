// 顶层布局：侧边导航 + 全局操作人输入框 + 主内容区。
// 写操作必须带 operator，这里提供全局录入，空值时输入框旁给出提示。

import { NavLink, Outlet } from 'react-router-dom'
import { useOperator } from '../state/operator'

// 导航项定义（路径 + 中文名）
const NAV_ITEMS: Array<{ to: string; label: string }> = [
  { to: '/configs', label: '配置中心' },
  { to: '/instances', label: '实例与健康' },
  { to: '/zones', label: 'zone 分配' },
  { to: '/audits', label: '审计日志' },
  { to: '/namespaces', label: '环境管理' },
]

export default function Layout() {
  const [operator, setOperator] = useOperator()

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
          <label className="operator-label" htmlFor="operator-input">
            操作人
          </label>
          <input
            id="operator-input"
            className="operator-input"
            placeholder="必填：用于写操作"
            value={operator}
            onChange={(e) => setOperator(e.target.value)}
          />
          {!operator && <div className="operator-hint">发布/回滚/改派/下线前请先填写操作人</div>}
        </div>
      </aside>
      <main className="content">
        <Outlet />
      </main>
    </div>
  )
}
