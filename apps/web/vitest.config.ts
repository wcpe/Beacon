import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// vitest 独立配置：jsdom 环境跑组件与逻辑测试，测试端与浏览器共享 @beacon/devmock 的 handlers 与场景
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
  },
})
