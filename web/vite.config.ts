import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Beacon 管理台前端构建配置。
// 产物输出到 dist/，由控制面 `go:embed all:dist` 内嵌进单二进制。
export default defineConfig({
  // react：JSX 转换；tailwindcss：Tailwind v4 的 Vite 插件（构建期产出静态 CSS）
  plugins: [react(), tailwindcss()],
  // 资源根路径：与控制面同端口同根挂载
  base: '/',
  // 路径别名：@ 指向 src（shadcn-ui 组件按 @/ 引用）
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    // 构建产物目录，对应 .gitignore 的 /web/dist/
    outDir: 'dist',
    // 不清空 outDir：保留入库的 dist/.gitkeep 占位（供无产物时 go:embed 仍可编译）
    emptyOutDir: false,
  },
  server: {
    // 开发期把后端两类前缀代理到本地控制面，避免跨域
    proxy: {
      // 管理台 API（React 管理台调用）
      '/admin/v1': 'http://localhost:8848',
      // agent 接入 API（仅调试用，开发期一并代理）
      '/beacon/v1': 'http://localhost:8848',
    },
  },
})
