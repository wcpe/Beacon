// vitest 测试环境初始化：
// 1) 注册 jest-dom 自定义断言（toBeInTheDocument / toHaveClass 等）；
// 2) 每个用例结束后卸载已渲染组件，避免跨用例 DOM 残留（未启用 globals 时 RTL 不会自动清理）。
// 由 vite.config.ts 的 test.setupFiles 在每个测试文件前加载。
import '@testing-library/jest-dom/vitest'
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'
// i18n 初始化（FR-50，见 ADR-0033）：测试环境同步初始化，保证 t() 同步返回 zh-CN 文案、不出裸 key
import '../i18n'

// jsdom 不实现 ResizeObserver：radix ScrollArea 等组件在内容溢出时会用到它，缺失会在渲染时抛错。
// 提供最小空实现垫片，使依赖滚动条的组件测试可正常渲染（不影响断言）。
if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

afterEach(() => {
  cleanup()
})
