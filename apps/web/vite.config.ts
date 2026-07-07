import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import { createRequire } from 'node:module'
import { copyFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const require = createRequire(import.meta.url)
const mswWorkerSource = require.resolve('msw/mockServiceWorker.js')
const mswWorkerTarget = fileURLToPath(new URL('public/mockServiceWorker.js', import.meta.url))

const syncMswWorker = {
  name: 'beacon-sync-msw-worker',
  configResolved() {
    mkdirSync(dirname(mswWorkerTarget), { recursive: true })
    copyFileSync(mswWorkerSource, mswWorkerTarget)
  },
}

const keepDistPlaceholder = {
  name: 'beacon-keep-dist-placeholder',
  closeBundle() {
    writeFileSync(fileURLToPath(new URL('dist/.gitkeep', import.meta.url)), '')
  },
}

export default defineConfig({
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
})
