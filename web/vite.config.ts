/// <reference types="vitest/config" />
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
  // 路径别名：@ 指向 src；@beacon/ui 指向内部 UI 包源码
  resolve: {
    alias: [
      {
        find: '@beacon/ui/styles.css',
        replacement: fileURLToPath(new URL('./packages/ui/src/styles.css', import.meta.url)),
      },
      {
        find: '@beacon/ui',
        replacement: fileURLToPath(new URL('./packages/ui/src/index.ts', import.meta.url)),
      },
      { find: '@', replacement: fileURLToPath(new URL('./src', import.meta.url)) },
    ],
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
  // 单元测试配置（vitest）：仅本地/CI 跑测试时生效，不影响 go:embed 的生产构建
  test: {
    // 组件测试需要 DOM 环境
    environment: 'jsdom',
    // 每个测试文件前加载 jest-dom 断言
    setupFiles: ['./src/test/setup.ts'],
    // 测试用例文件范围
    include: ['src/**/*.test.{ts,tsx}'],
    // 关闭 CSS 处理，加速且避免 Tailwind v4 插件介入测试
    css: false,
    // 覆盖率门禁：基于 v8 引擎统计，CI 与本地共用
    coverage: {
      // 使用 V8 原生覆盖率（无需 babel 插桩，速度快）
      provider: 'v8',
      // text：控制台汇总；html：本地可视化产物（输出到 coverage/）
      reporter: ['text', 'html'],
      // 纳入统计的源码范围：仅业务源码
      include: ['src/**/*.{ts,tsx}'],
      // 排除不计覆盖的文件，理由见各行注释
      exclude: [
        // 测试文件本身不计入覆盖率
        '**/*.test.{ts,tsx}',
        // 测试脚手架（setup、工具），非业务代码
        'src/test/**',
        // 假后端 mock 数据/处理器，仅开发期使用，无需覆盖
        'src/api/mock/**',
        // 类型声明文件无可执行语句
        '**/*.d.ts',
        // 应用入口仅做挂载与初始化，难以单测且无业务逻辑
        'src/main.tsx',
        // 纯类型定义文件（接口/类型别名），无运行时语句
        '**/types.ts',
      ],
      // 阈值门禁：达不到则 vitest run --coverage 失败
      thresholds: {
        // 行覆盖率底线
        lines: 70,
        // 语句覆盖率底线
        statements: 70,
        // 函数覆盖率底线
        functions: 70,
        // 分支覆盖率：分支覆盖通常偏低，按实测定一个不低于实测的整十数
        branches: 70,
      },
    },
  },
})
