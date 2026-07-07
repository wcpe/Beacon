// vitest 全局装配：注入 jest-dom 断言扩展
import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// 未开启 vitest globals，React Testing Library 不会自动清理，这里显式注册
afterEach(() => {
  cleanup()
})
