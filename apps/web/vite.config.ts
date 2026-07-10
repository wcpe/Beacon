import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import { createRequire } from 'node:module'
import { copyFileSync, existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const require = createRequire(import.meta.url)
const mswWorkerSource = require.resolve('msw/mockServiceWorker.js')
const mswWorkerTarget = fileURLToPath(new URL('public/mockServiceWorker.js', import.meta.url))

const keepDistPlaceholder = {
  name: 'beacon-keep-dist-placeholder',
  closeBundle() {
    writeFileSync(fileURLToPath(new URL('dist/.gitkeep', import.meta.url)), '')
  },
}

// 演示模式（FR-159）门控：仅 dev server 与 `vite build --mode demo` 产出 mock。
// 常规 `vite build`（发布产物，由控制面 go:embed 内嵌）绝不落 mockServiceWorker.js，
// 否则 Service Worker 会拦截 /admin/* 返回假数据、使真实后端不可达。
export default defineConfig(({ command, mode }) => {
  const demoMode = command === 'serve' || mode === 'demo'

  const syncMswWorker = {
    name: 'beacon-sync-msw-worker',
    configResolved() {
      if (demoMode) {
        mkdirSync(dirname(mswWorkerTarget), { recursive: true })
        copyFileSync(mswWorkerSource, mswWorkerTarget)
      } else if (existsSync(mswWorkerTarget)) {
        // 清掉上次 demo 构建 / dev 留下的 worker，避免被 public 目录带进 dist
        rmSync(mswWorkerTarget)
      }
    },
  }

  return {
    root: fileURLToPath(new URL('.', import.meta.url)),
    plugins: [react(), tailwindcss(), syncMswWorker, keepDistPlaceholder],
    resolve: {
      alias: [
        {
          find: '@beacon/ui/styles.css',
          replacement: fileURLToPath(new URL('../../packages/ui/src/styles.css', import.meta.url)),
        },
        {
          find: '@beacon/ui',
          replacement: fileURLToPath(new URL('../../packages/ui/src/index.ts', import.meta.url)),
        },
      ],
    },
    build: {
      outDir: 'dist',
      emptyOutDir: true,
    },
  }
})
