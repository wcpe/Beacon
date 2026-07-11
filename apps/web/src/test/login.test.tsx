// /login 登录页 mockup 测试（FR-179 阶段 A）：渲染品牌与表单、点登录进加载态、
// 演示成功 / 失败四态。本阶段不接真鉴权，故无需 mock 服务端。
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'

import LoginPage from '../pages/login'
import '../i18n'

function renderLogin() {
  return render(
    <MemoryRouter>
      <LoginPage />
    </MemoryRouter>,
  )
}

describe('/login 登录页 mockup', () => {
  it('渲染品牌标题与用户名 / 口令表单', () => {
    renderLogin()
    expect(screen.getByText('Beacon')).toBeInTheDocument()
    expect(screen.getByLabelText('用户名')).toBeInTheDocument()
    expect(screen.getByLabelText('口令')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '登录' })).toBeInTheDocument()
  })

  it('填表点登录后进入加载态（按钮禁用 + 登录中文案）', async () => {
    const user = userEvent.setup()
    renderLogin()

    await user.type(screen.getByLabelText('用户名'), 'admin')
    await user.type(screen.getByLabelText('口令'), 'secret')
    await user.click(screen.getByRole('button', { name: '登录' }))

    const loadingButton = screen.getByRole('button', { name: '登录中…' })
    expect(loadingButton).toBeDisabled()
  })

  it('常规口令演示成功态（mockup 不跳转）', async () => {
    const user = userEvent.setup()
    renderLogin()

    await user.type(screen.getByLabelText('用户名'), 'admin')
    await user.type(screen.getByLabelText('口令'), 'goodpass')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByRole('status')).toHaveTextContent('登录成功')
  })

  it('口令含 bad 演示脱敏失败态', async () => {
    const user = userEvent.setup()
    renderLogin()

    await user.type(screen.getByLabelText('用户名'), 'admin')
    await user.type(screen.getByLabelText('口令'), 'badpass')
    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('有误')
  })

  it('用户名或口令为空时点登录提示必填', async () => {
    const user = userEvent.setup()
    renderLogin()

    await user.click(screen.getByRole('button', { name: '登录' }))

    expect(screen.getByRole('alert')).toHaveTextContent('请输入用户名与口令')
  })
})
