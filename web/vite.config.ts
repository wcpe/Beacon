import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Beacon 管理台前端构建配置。
// 产物输出到 dist/，由控制面 `go:embed all:dist` 内嵌进单二进制。
export default defineConfig({
  plugins: [react()],
  // 资源根路径：与控制面同端口同根挂载
  base: '/',
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
      '/admin/v1': 'http://localhost:8080',
      // agent 接入 API（仅调试用，开发期一并代理）
      '/beacon/v1': 'http://localhost:8080',
    },
  },
})
