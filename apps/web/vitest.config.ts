import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// vitest 独立配置：jsdom 环境跑组件与逻辑测试，测试端与浏览器共享 @beacon/devmock 的 handlers 与场景
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    // 单测默认 5s 在 47 个文件并行（jsdom + MSW）下不够：同一用例单跑 1.1s、并行 2.0–4.0s，
    // 每轮全量都有 9～14 条落在 4–5s，最慢 4957ms（距失败线仅 28ms），冷启动那轮越线 3 条。
    // 与既有约定一致（30 处用例已显式传 20_000）；只放宽上限，不改任何断言。
    testTimeout: 20_000,
  },
})
