// 账户菜单（FR-121）：整宽顶栏右上角首字母头像 + 下拉（操作人全名 + 登出）。
// FR-121 从侧栏底部「当前操作人 / 登出」移来；登出逻辑（best-effort 审计 + 清登录态 + 跳登录）随之内聚到此。
// 操作者身份由登录令牌决定（FR-11），此处仅呈现与触发登出，不持有登录态。

import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { LogOut } from 'lucide-react'
import { clearAuth, useAuth } from '@/state/auth'
import { logout } from '@/api/client'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'

export default function OperatorMenu() {
  const { t } = useTranslation()
  const { operator } = useAuth()
  const navigate = useNavigate()
  // 首字母头像：取操作人首字符大写；为空时回退占位「?」
  const initial = (operator || '').trim().charAt(0).toUpperCase() || '?'

  async function onLogout() {
    // 先 best-effort 记一条登出审计（需当前令牌）；无论成败都清本地登录态并跳登录——登出绝不被阻断
    try {
      await logout()
    } catch {
      // 令牌已过期等场景审计失败，忽略：登出是本地动作，不依赖后端成功
    }
    clearAuth()
    navigate('/login', { replace: true })
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('layout.accountMenu')}
          title={operator || '-'}
          className="flex size-7 shrink-0 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground transition-opacity hover:opacity-90 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          {initial}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-48">
        <DropdownMenuLabel className="font-normal">
          <div className="text-xs text-muted-foreground">{t('layout.currentOperator')}</div>
          <div className="mt-0.5 break-all text-sm font-medium">{operator || '-'}</div>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={onLogout}>
          <LogOut aria-hidden className="size-4" />
          {t('layout.logout')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
