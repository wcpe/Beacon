// 登出测试（FR-179）：页眉操作人区展示当前操作者；点登出 → POST /admin/v1/auth/logout +
// 清本地令牌 → 跳登录。审计失败也照常清令牌（onSettled 收尾），此处覆盖成功路径。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import OperatorMenu from '../../shell/operator-menu'
import { clearAuth, currentToken, setAuth } from '../../state/auth'
import { fakeResponse, renderWithClient } from './harness'

function renderMenu() {
  return renderWithClient(
    <MemoryRouter initialEntries={['/dashboard']}>
      <Routes>
        <Route element={<OperatorMenu />} path="/dashboard" />
        <Route element={<div>登录页占位</div>} path="/login" />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  localStorage.clear()
  clearAuth()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('页眉登出', () => {
  it('展示当前操作者名', () => {
    setAuth('tok-abc', 'admin')
    renderMenu()
    expect(screen.getByText('admin')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '登出' })).toBeInTheDocument()
  })

  it('点登出：清令牌并跳登录', async () => {
    setAuth('tok-abc', 'admin')
    const fetchMock = vi.fn().mockResolvedValue(fakeResponse(204))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    renderMenu()

    await user.click(screen.getByRole('button', { name: '登出' }))

    expect(await screen.findByText('登录页占位')).toBeInTheDocument()
    await waitFor(() => {
      expect(currentToken()).toBe('')
    })
    expect(fetchMock).toHaveBeenCalledWith(
      '/admin/v1/auth/logout',
      expect.objectContaining({ method: 'POST' }),
    )
  })
})
