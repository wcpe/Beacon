// 页眉用户菜单（FR-179 + FR-187）：头像触发下拉，展示名/角色与登出。
// 登出 = POST /admin/v1/auth/logout（记审计）+ 清本地令牌 → 跳登录；令牌无状态，
// 即便登出审计失败也照常清本地令牌，故用 onSettled 统一收尾。
import { useTranslation } from 'react-i18next'
import { useMutation } from '@tanstack/react-query'
import { ChevronDown, LogOut } from 'lucide-react'
import { useNavigate } from 'react-router-dom'

import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@beacon/ui'

import { logout } from '../api/auth'
import { clearAuth, useAuth } from '../state/auth'

function avatarGlyph(name: string): string {
  const trimmed = name.trim()
  if (trimmed === '') {
    return '运'
  }
  return trimmed.slice(0, 1).toUpperCase()
}

export default function OperatorMenu() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { operator } = useAuth()
  const displayName = operator === '' ? 'admin' : operator

  const logoutMut = useMutation({
    mutationFn: () => logout(),
    onSettled: () => {
      clearAuth()
      navigate('/login', { replace: true })
    },
  })

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="gap-1.5 px-1.5"
          aria-label={t('common.header.accountMenu')}
        >
          <span className="grid size-7 place-items-center rounded-full bg-gradient-to-br from-[oklch(0.62_0.19_282)] to-[oklch(0.62_0.2_305)] text-[11px] font-bold text-white">
            {avatarGlyph(displayName)}
          </span>
          <span className="hidden max-w-[7rem] truncate text-[13px] text-ink-2 sm:inline">
            {displayName}
          </span>
          <ChevronDown className="size-3.5 text-ink-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-44">
        <DropdownMenuLabel className="font-normal">
          <div className="text-sm font-semibold text-ink-1">{displayName}</div>
          <div className="text-[11px] text-ink-4">{t('common.superAdmin')}</div>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          disabled={logoutMut.isPending}
          onSelect={() => {
            logoutMut.mutate()
          }}
        >
          <LogOut className="size-4" />
          {t('auth.logout.action')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
