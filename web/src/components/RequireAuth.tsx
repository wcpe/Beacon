// 路由守卫：未登录（无令牌）访问受保护页时跳 /login，并记下来访页便于登录后跳回。

import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from '../state/auth'

export default function RequireAuth() {
  const { token } = useAuth()
  const location = useLocation()
  if (!token) {
    // 记录来访路径，登录成功后跳回
    return <Navigate to="/login" replace state={{ from: location }} />
  }
  return <Outlet />
}
