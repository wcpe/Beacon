// /login 登录页真鉴权测试（FR-179 阶段 B）：真实提交 POST /admin/v1/auth/login，
// 成功存令牌 + 回跳、失败展示后端脱敏文案、空校验、加载态。用假 fetch 控制后端响应。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import LoginPage from '../../pages/login'
import { clearAuth, currentToken, setOnUnauthorized } from '../../state/auth'
import { fakeResponse, renderWithClient } from './harness'

// 在 /login 渲染登录页，并提供 / 目标路由占位以断言成功后回跳
function renderLogin() {
  return renderWithClient(
    <MemoryRouter initialEntries={['/login']}>
      <Routes>
        <Route element={<LoginPage />} path="/login" />
        <Route element={<div>运维总览首页</div>} path="/" />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  localStorage.clear()
  clearAuth()
  // 复位全局 401 回调为空操作，避免跨用例串扰
  setOnUnauthorized(() => {
    // 空操作
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('/login 登录页真鉴权', () => {
  it('渲染品牌标题与用户名 / 口令表单', () => {
    renderLogin()
    expect(screen.getByText('Beacon')).toBeInTheDocument()
    expect(screen.getByLabelText('用户名')).toBeInTheDocument()
    expect(screen.getByLabelText('口令')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '登录' })).toBeInTheDocument()
  })

  it('登录成功：存令牌并回跳首页', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      fakeResponse(200, { token: 'tok-abc', operator: 'admin' }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    renderLogin()

    await user.type(screen.getByLabelText('用户名'), 'admin')
    await user.type(screen.getByLabelText('口令'), 'secret')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByText('运维总览首页')).toBeInTheDocument()
    expect(currentToken()).toBe('tok-abc')
    // 校验请求走 POST /admin/v1/auth/login，请求体含凭据
    expect(fetchMock).toHaveBeenCalledWith(
      '/admin/v1/auth/login',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ username: 'admin', password: 'secret' }) }),
    )
  })

  it('凭据错：展示后端脱敏文案并清令牌', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      fakeResponse(401, { code: 'BAD_CREDENTIALS', message: '用户名或口令错误' }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    renderLogin()

    await user.type(screen.getByLabelText('用户名'), 'admin')
    await user.type(screen.getByLabelText('口令'), 'wrong')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('用户名或口令错误')
    expect(currentToken()).toBe('')
  })

  it('用户名或口令为空时点登录提示必填，不发请求', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    renderLogin()

    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(screen.getByRole('alert')).toHaveTextContent('请输入用户名与口令')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('提交中进入加载态（按钮禁用 + 登录中文案）', async () => {
    // 永不 resolve 的 fetch 让加载态可断言
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    renderLogin()

    await user.type(screen.getByLabelText('用户名'), 'admin')
    await user.type(screen.getByLabelText('口令'), 'secret')
    await user.click(screen.getByRole('button', { name: '登录' }))

    await waitFor(() => {
      expect(screen.getByRole('button', { name: '登录中…' })).toBeDisabled()
    })
  })
})
