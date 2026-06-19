// vitest 测试环境初始化：
// 1) 注册 jest-dom 自定义断言（toBeInTheDocument / toHaveClass 等）；
// 2) 每个用例结束后卸载已渲染组件，避免跨用例 DOM 残留（未启用 globals 时 RTL 不会自动清理）。
// 由 vite.config.ts 的 test.setupFiles 在每个测试文件前加载。
import '@testing-library/jest-dom/vitest'
import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

afterEach(() => {
  cleanup()
})
