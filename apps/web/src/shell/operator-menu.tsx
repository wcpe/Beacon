// 页眉操作人区（FR-179，真鉴权模式下展示）：显示当前登录操作者 + 登出入口。
// 登出 = POST /admin/v1/auth/logout（记审计）+ 清本地令牌 → 跳登录；令牌无状态，
// 即便登出审计失败也照常清本地令牌（前端清即失效），故用 onSettled 统一收尾。
import { useTranslation } from 'react-i18next'

import { useMutation } from '@tanstack/react-query'
import { LogOut, UserRound } from 'lucide-react'
import { useNavigate } from 'react-router-dom'

import { Button } from '@beacon/ui'

import { logout } from '../api/auth'
import { clearAuth, useAuth } from '../state/auth'

export default function OperatorMenu() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { operator } = useAuth()

  const logoutMut = useMutation({
    mutationFn: () => logout(),
    onSettled: () => {
      clearAuth()
      navigate('/login', { replace: true })
    },
  })

  return (
    <div className="flex items-center gap-2">
      <span className="flex items-center gap-1.5 text-[13px] text-ink-3">
        <UserRound className="size-4" />
        {operator}
      </span>
      <Button
        disabled={logoutMut.isPending}
        size="sm"
        variant="ghost"
        onClick={() => {
          logoutMut.mutate()
        }}
      >
        <LogOut className="size-4" />
        {t('auth.logout.action')}
      </Button>
    </div>
  )
}
