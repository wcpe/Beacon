// 登录页（/login，FR-179）：全屏、无侧栏无页眉，独立于 AppShell。
// 居中卡片——灯塔品牌 logo + 标题 + 用户名 / 口令输入 + 登录主按钮 + 错误提示区 + 环境副文案。
// 阶段 B 接真鉴权：真实 POST /admin/v1/auth/login，成功存令牌并回跳来访页（或首页），
// 失败内联展示后端脱敏文案（ADR-0057）。视觉 / 布局 / 品牌区沿用阶段 A 拍板稿，仅换提交行为。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { useMutation } from '@tanstack/react-query'
import { Loader2, TriangleAlert } from 'lucide-react'
import { useLocation, useNavigate } from 'react-router-dom'

import { Button, Card, CardContent, CardHeader, CardTitle, Input, Label } from '@beacon/ui'

import { ApiClientError } from '../api/cluster'
import { login } from '../api/auth'
import { setAuth } from '../state/auth'

// 路由守卫 / 401 重定向时塞进 location.state 的来访信息
interface FromState {
  from?: { pathname: string }
}

export default function LoginPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const location = useLocation()

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)

  // 登录成功后的目标：优先回被拦截的来访页，否则运维总览首页
  const target = (location.state as FromState | null)?.from?.pathname ?? '/'

  const loginMut = useMutation({
    mutationFn: () => login(username.trim(), password),
    onSuccess: (result) => {
      setAuth(result.token, result.operator)
      navigate(target, { replace: true })
    },
    onError: (err) => {
      // 后端脱敏文案直接展示；非 ApiClientError 兜底通用失败文案。
      setError(err instanceof ApiClientError ? err.message : t('auth.login.failed'))
    },
  })

  // 真实登录提交：前端仅校验非空，凭据校验交由后端（凭据错回 401，展示脱敏 message）。
  function handleSubmit(event: React.FormEvent) {
    event.preventDefault()
    if (username.trim() === '' || password === '') {
      setError(t('auth.login.missingCredentials'))
      return
    }
    setError(null)
    loginMut.mutate()
  }

  const loading = loginMut.isPending

  return (
    <div className="app-bg flex min-h-screen flex-col items-center justify-center gap-4 p-4 text-foreground">
      <Card className="w-full max-w-[380px] shadow-pop">
        <CardHeader className="items-center gap-2 text-center">
          {/* 品牌横向锁定：灯塔 mark（放大、无盒子）+ Beacon 字标并排 */}
          <div className="flex items-center justify-center gap-2.5">
            <img alt="" className="size-11" src="/logo.svg" />
            <CardTitle className="text-[22px] tracking-[0.2px]">{t('auth.login.title')}</CardTitle>
          </div>
          <p className="text-[13px] text-ink-3">{t('auth.login.subtitle')}</p>
        </CardHeader>

        <CardContent>
          <form className="grid gap-3.5" onSubmit={handleSubmit}>
            <div className="grid gap-1.5">
              <Label htmlFor="login-username">{t('auth.login.username')}</Label>
              <Input
                autoComplete="username"
                autoFocus
                disabled={loading}
                id="login-username"
                placeholder={t('auth.login.usernamePlaceholder')}
                value={username}
                onChange={(event) => {
                  setUsername(event.target.value)
                }}
              />
            </div>

            <div className="grid gap-1.5">
              <Label htmlFor="login-password">{t('auth.login.password')}</Label>
              <Input
                autoComplete="current-password"
                disabled={loading}
                id="login-password"
                placeholder={t('auth.login.passwordPlaceholder')}
                type="password"
                value={password}
                onChange={(event) => {
                  setPassword(event.target.value)
                }}
              />
            </div>

            {/* 失败提示区固定高度占位，避免错误出现时卡片高度抖动 */}
            <div className="min-h-[40px]" aria-live="polite">
              {error !== null && (
                <div
                  className="flex items-start gap-2 rounded-lg border border-crit-bd bg-crit-bg px-3 py-2 text-[13px] text-crit"
                  role="alert"
                >
                  <TriangleAlert className="mt-px size-4 shrink-0" />
                  <span>{error}</span>
                </div>
              )}
            </div>

            <Button className="mt-0.5 h-10 w-full rounded-lg" disabled={loading} type="submit">
              {loading ? (
                <>
                  <Loader2 className="size-4 animate-spin" />
                  {t('auth.login.submitting')}
                </>
              ) : (
                t('auth.login.submit')
              )}
            </Button>
          </form>
        </CardContent>
      </Card>

      {/* 卡片下方环境副文案 */}
      <p className="text-[11px] text-ink-4">{t('auth.login.envHint')}</p>
    </div>
  )
}
