// 登录页：账号口令 → 令牌（存 sessionStorage）。所有写操作前置（FR-11 鉴权）。
// 登录成功后跳回来访页（被 401 拦截或路由守卫重定向时带的 from），默认回配置中心。

import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useLocation, useNavigate } from 'react-router-dom'
import { login } from '../api/client'
import { setAuth, useAuth } from '../state/auth'
import { useMessage } from '../components/useMessage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

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
    <div className="flex min-h-screen items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="text-center text-lg">Beacon 管理台</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="l-username">账号</Label>
              <Input
                id="l-username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="username"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="l-password">口令</Label>
              <Input
                id="l-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
              />
            </div>
            <Button type="submit" className="w-full" disabled={loginMut.isPending}>
              {loginMut.isPending ? '登录中…' : '登录'}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
