// 路由守卫测试（FR-179）：未登录且非 demo → 跳登录并记来访路径；已登录 → 放行；
// demo 模式 → 免登录放行。用 vi.stubEnv 控制 isDemoMode 判据（DEV / MODE）。
import { screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import RequireAuth from '../../shell/require-auth'
import { clearAuth, setAuth } from '../../state/auth'
import { renderWithClient } from './harness'

// 登录页占位：把 location.state.from.pathname 渲染出来，验证来访路径被记住
function LoginStub() {
  const location = useLocation()
  const state = location.state as { from?: { pathname: string } } | null
  return <div>登录页 from={state?.from?.pathname ?? '无'}</div>
}

// 在受保护路径下渲染守卫，提供 /login 占位路由
function renderGuardedAt(path: string) {
  return renderWithClient(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          element={
            <RequireAuth>
              <div>受保护内容</div>
            </RequireAuth>
          }
          path="/*"
        />
        <Route element={<LoginStub />} path="/login" />
      </Routes>
    </MemoryRouter>,
  )
}

beforeEach(() => {
  localStorage.clear()
  clearAuth()
})

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('路由守卫 RequireAuth', () => {
  it('未登录且非 demo：跳登录并记住来访路径', () => {
    vi.stubEnv('DEV', false)
    vi.stubEnv('MODE', 'production')
    renderGuardedAt('/servers')
    expect(screen.getByText('登录页 from=/servers')).toBeInTheDocument()
    expect(screen.queryByText('受保护内容')).not.toBeInTheDocument()
  })

  it('已登录：放行受保护内容', () => {
    vi.stubEnv('DEV', false)
    vi.stubEnv('MODE', 'production')
    setAuth('tok-abc', 'admin')
    renderGuardedAt('/servers')
    expect(screen.getByText('受保护内容')).toBeInTheDocument()
  })

  it('demo 模式：未登录也放行（免登录门控不回归）', () => {
    vi.stubEnv('DEV', true)
    vi.stubEnv('MODE', 'development')
    renderGuardedAt('/servers')
    expect(screen.getByText('受保护内容')).toBeInTheDocument()
  })
})
