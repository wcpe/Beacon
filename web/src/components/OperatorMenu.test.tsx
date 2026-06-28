// OperatorMenu 单测（FR-121）：首字母头像 + 下拉（操作人全名 + 登出）+ 登出动作。
// 登出从侧栏底部移至右上角账户菜单，登出逻辑内聚到本组件。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'

vi.mock('@/api/client', () => ({
  logout: vi.fn().mockResolvedValue(undefined),
}))

const navigateSpy = vi.fn()
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom')
  return { ...actual, useNavigate: () => navigateSpy }
})

import OperatorMenu from './OperatorMenu'
import { logout } from '@/api/client'
import { setAuth, clearAuth, currentToken } from '@/state/auth'

function renderMenu() {
  return render(
    <MemoryRouter>
      <OperatorMenu />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  navigateSpy.mockReset()
  vi.mocked(logout).mockClear()
  clearAuth()
})

describe('OperatorMenu', () => {
  it('头像显示操作人首字母（大写）', () => {
    setAuth('tok', 'admin')
    renderMenu()
    const avatar = screen.getByRole('button', { name: '账户菜单' })
    expect(avatar.textContent).toBe('A')
  })

  it('操作人为空时头像回退占位「?」', () => {
    clearAuth()
    renderMenu()
    expect(screen.getByRole('button', { name: '账户菜单' }).textContent).toBe('?')
  })

  it('点开下拉显示操作人全名 + 登出项', async () => {
    setAuth('tok', 'admin')
    renderMenu()
    await userEvent.click(screen.getByRole('button', { name: '账户菜单' }))
    // 下拉内含当前操作人标签 + 全名 + 登出项
    expect(await screen.findByText('当前操作人')).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: /登出/ })).toBeInTheDocument()
  })

  it('点登出：记审计 + 清登录态 + 跳登录', async () => {
    setAuth('tok', 'admin')
    renderMenu()
    await userEvent.click(screen.getByRole('button', { name: '账户菜单' }))
    await userEvent.click(await screen.findByRole('menuitem', { name: /登出/ }))
    expect(logout).toHaveBeenCalledTimes(1)
    expect(navigateSpy).toHaveBeenCalledWith('/login', { replace: true })
    // 登录态已清空
    expect(currentToken()).toBe('')
  })
})
