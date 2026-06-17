// 登录页：账号口令 → 令牌（存 sessionStorage）。所有写操作前置（FR-11 鉴权）。
// 登录成功后跳回来访页（被 401 拦截或路由守卫重定向时带的 from），默认回配置中心。

import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useLocation, useNavigate } from 'react-router-dom'
import { login } from '../api/client'
import { setAuth, useAuth } from '../state/auth'
import MessageBar from '../components/MessageBar'
import { useMessage } from '../components/useMessage'

// 路由守卫重定向时塞进 location.state 的来访信息
interface FromState {
  from?: { pathname: string }
}

export default function LoginPage() {
  const msg = useMessage()
  const navigate = useNavigate()
  const location = useLocation()
  const { token } = useAuth()

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')

  // 登录前若已有令牌（如手动访问 /login），直接回首页
  if (token) {
    navigate('/configs', { replace: true })
  }

  // 登录成功后的目标：优先回被拦截的来访页，否则配置中心
  const target = (location.state as FromState | null)?.from?.pathname ?? '/configs'

  const loginMut = useMutation({
    mutationFn: () => login(username.trim(), password),
    onSuccess: (r) => {
      setAuth(r.token, r.operator)
      navigate(target, { replace: true })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!username.trim() || !password) {
      msg.showError('请填写账号与口令')
      return
    }
    loginMut.mutate()
  }

  return (
    <div className="login-shell">
      <form className="login-card" onSubmit={onSubmit}>
        <div className="login-brand">Beacon 管理台</div>
        <MessageBar message={msg.message} onClose={msg.clear} />
        <label>
          账号
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
        </label>
        <label>
          口令
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
          />
        </label>
        <button type="submit" disabled={loginMut.isPending}>
          {loginMut.isPending ? '登录中…' : '登录'}
        </button>
      </form>
    </div>
  )
}
