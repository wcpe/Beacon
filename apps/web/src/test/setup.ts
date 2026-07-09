// vitest 全局装配：注入 jest-dom 断言扩展
import '@testing-library/jest-dom/vitest'
import { cleanup, configure } from '@testing-library/react'
import { afterEach } from 'vitest'

// findBy*/waitFor 默认 1s 在 20 个测试文件并行的 jsdom + MSW 负载下偶发不够（如 settings
// 页多查询首屏），导致脆失败；校准到 3s 只放宽等待时限、不削弱断言。
configure({ asyncUtilTimeout: 3000 })

// 未开启 vitest globals，React Testing Library 不会自动清理，这里显式注册
afterEach(() => {
  cleanup()
})

// radix-ui（Select 等浮层组件）依赖的浏览器 API 在 jsdom 中缺失，统一打桩
if (!('ResizeObserver' in globalThis)) {
  class ResizeObserverStub {
    observe(): void {
      // jsdom 无布局，桩实现即可
    }

    unobserve(): void {
      // jsdom 无布局，桩实现即可
    }

    disconnect(): void {
      // jsdom 无布局，桩实现即可
    }
  }
  Object.defineProperty(globalThis, 'ResizeObserver', { value: ResizeObserverStub })
}

Object.assign(window.HTMLElement.prototype, {
  scrollIntoView: () => {
    // jsdom 无滚动布局，桩实现即可
  },
  hasPointerCapture: () => false,
  setPointerCapture: () => {
    // jsdom 无指针捕获，桩实现即可
  },
  releasePointerCapture: () => {
    // jsdom 无指针捕获，桩实现即可
  },
})
