// 登录页（/login，FR-179 阶段 A mockup）：全屏、无侧栏无页眉，独立于 AppShell。
// 居中卡片——灯塔品牌 logo + 标题 + 用户名 / 口令输入 + 登录主按钮 + 错误 / 成功提示区 + 环境副文案。
// 本阶段仅视觉与四态交互演示，用纯前端 mock 模拟提交：不接真实 /admin/v1/auth/login、
// 不存令牌、不注入 Authorization、不做路由守卫（均为阶段 B 内容）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CircleCheck, Loader2, TriangleAlert } from 'lucide-react'

import { Button, Card, CardContent, CardHeader, CardTitle, Input, Label } from '@beacon/ui'

// 提交状态机：空态 / 加载 / 成功 / 失败（供四态评审）
type SubmitStatus = 'idle' | 'loading' | 'success' | 'error'

// mockup 模拟提交时延（毫秒）：让评审者看得到加载态
const MOCK_SUBMIT_DELAY_MS = 1000

export default function LoginPage() {
  const { t } = useTranslation()

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [status, setStatus] = useState<SubmitStatus>('idle')
  const [message, setMessage] = useState<string | null>(null)

  // 纯前端 mock 提交：校验非空 → 加载 1s → 按约定演示输入切成功 / 失败态，不真实跳转。
  function handleSubmit(event: React.FormEvent) {
    event.preventDefault()
    if (username.trim() === '' || password === '') {
      setStatus('error')
      setMessage(t('auth.login.missingCredentials'))
      return
    }
    setStatus('loading')
    setMessage(null)
    window.setTimeout(() => {
      if (password.includes('bad')) {
        setStatus('error')
        setMessage(t('auth.login.errorMock'))
      } else {
        setStatus('success')
        setMessage(t('auth.login.successMock'))
      }
    }, MOCK_SUBMIT_DELAY_MS)
  }

  const loading = status === 'loading'

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

            {/* 失败提示区（脱敏文案样式：crit 浅底 + 同色描边，只显脱敏后的安全文案） */}
            {status === 'error' && message !== null && (
              <div
                className="flex items-start gap-2 rounded-lg border border-crit-bd bg-crit-bg px-3 py-2 text-[13px] text-crit"
                role="alert"
              >
                <TriangleAlert className="mt-px size-4 shrink-0" />
                <span>{message}</span>
              </div>
            )}

            {/* 成功提示区（mockup 不跳转，仅演示成功态：ok 浅底 + 同色描边） */}
            {status === 'success' && message !== null && (
              <div
                className="flex items-start gap-2 rounded-lg border border-ok-bd bg-ok-bg px-3 py-2 text-[13px] text-ok"
                role="status"
              >
                <CircleCheck className="mt-px size-4 shrink-0" />
                <span>{message}</span>
              </div>
            )}

            <Button className="mt-0.5 w-full" disabled={loading} type="submit">
              {loading ? (
                <>
                  <Loader2 className="size-4 animate-spin" />
                  {t('auth.login.submitting')}
                </>
              ) : (
                t('auth.login.submit')
              )}
            </Button>

            {/* 演示提示：告知评审者如何触发失败态（阶段 B 接真鉴权后移除） */}
            <p className="text-center text-[11px] text-ink-4">{t('auth.login.mockTip')}</p>
          </form>
        </CardContent>
      </Card>

      {/* 卡片下方环境副文案 */}
      <p className="text-[11px] text-ink-4">{t('auth.login.envHint')}</p>
    </div>
  )
}
