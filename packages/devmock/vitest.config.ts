import { defineConfig } from 'vitest/config'

// devmock 测试跑 Node 环境（msw/node setupServer 拦截 fetch），无需 jsdom
export default defineConfig({
  test: {
    environment: 'node',
  },
})
