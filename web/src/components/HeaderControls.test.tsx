// HeaderControls 单测（FR-92）：主题切换、大屏入口链接。
import { describe, it, expect, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import HeaderControls from './HeaderControls'
import { currentPreferences, setTheme } from '@/state/preferences'

// 每个用例前复位为默认偏好（store 为模块单例）
beforeEach(() => {
  localStorage.clear()
  setTheme('light')
})

function renderControls() {
  return render(
    <MemoryRouter>
      <HeaderControls />
    </MemoryRouter>,
  )
}

describe('HeaderControls 主题切换', () => {
  it('浅色时点击切到暗色并持久化', async () => {
    renderControls()
    await userEvent.click(screen.getByRole('button', { name: '切换到暗色主题' }))
    expect(currentPreferences().theme).toBe('dark')
  })

  it('暗色时点击切回浅色', async () => {
    setTheme('dark')
    renderControls()
    await userEvent.click(screen.getByRole('button', { name: '切换到浅色主题' }))
    expect(currentPreferences().theme).toBe('light')
  })
})

describe('HeaderControls 大屏入口', () => {
  it('提供指向 /wallboard 的链接', () => {
    renderControls()
    const link = screen.getByRole('link', { name: '进入大屏' })
    expect(link).toHaveAttribute('href', '/wallboard')
  })
})
