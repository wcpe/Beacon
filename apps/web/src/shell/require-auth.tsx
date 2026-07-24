// 路由守卫（FR-179）：包住受保护路由（AppShell 那支）。无令牌一律跳登录并记住来访路径，
// 登录成功后回跳。demo 模式（FR-159）免登录——守卫直接放行，保持演示门控不回归。
import type { ReactElement } from 'react'

import { Navigate, useLocation } from 'react-router-dom'

import { isDemoMode } from '../demo-mode'
import { isAuthenticated } from '../state/auth'

export default function RequireAuth({ children }: { children: ReactElement }): ReactElement {
  const location = useLocation()
  if (!isDemoMode() && !isAuthenticated()) {
    // 记住来访路径（location.state.from），供登录页成功后回跳。
    return <Navigate replace state={{ from: location }} to="/login" />
  }
  return children
}
