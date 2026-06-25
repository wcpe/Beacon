// WallboardLayout 单测：进入大屏后整页（顶栏 + 页面底）固定深色（NOC 一致），
// 退出大屏 / 主题按钮按深色配色，且保持纯只读（守 FR-92，无额外操作入口）。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'

import WallboardLayout from './WallboardLayout'

function renderWallboard() {
  return render(
    <MemoryRouter initialEntries={['/wallboard']}>
      <Routes>
        <Route path="/wallboard" element={<WallboardLayout />}>
          <Route index element={<div>大屏内容</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )
}

describe('WallboardLayout 大屏固定深色', () => {
  it('整页根容器固定深色（bg-slate-950 / text-slate-100），不跟随主题', () => {
    renderWallboard()
    const root = screen.getByText('大屏内容').closest('div.flex.h-screen')
    expect(root).not.toBeNull()
    expect(root?.classList.contains('bg-slate-950')).toBe(true)
    expect(root?.classList.contains('text-slate-100')).toBe(true)
    // 不再使用跟随主题的 bg-background
    expect(root?.classList.contains('bg-background')).toBe(false)
  })

  it('顶栏标题在深色 header 内（与页面底同色系）', () => {
    renderWallboard()
    const title = screen.getByText('集群大屏')
    const header = title.closest('header')
    expect(header).not.toBeNull()
    // 顶栏与深色根同处一个固定深色容器
    expect(header?.closest('div.bg-slate-950')).not.toBeNull()
  })

  it('退出大屏链接按深色配色（深底浅字）', () => {
    renderWallboard()
    const exit = screen.getByRole('link', { name: /退出大屏/ })
    // 深色描边 + 浅字，非亮色块
    expect(exit.className).toMatch(/text-slate-200/)
    expect(exit.className).toMatch(/hover:bg-white\/10/)
  })
})
