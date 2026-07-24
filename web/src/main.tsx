import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider, MutationCache } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import { toast } from 'sonner'
import App from './App'
import { Toaster } from '@beacon/ui'
import { loader } from '@monaco-editor/react'
// i18n 初始化（FR-50，见 ADR-0033）：import 即同步完成初始化，须在渲染前先执行
import i18n from './i18n'
import { applyThemeToDocument, currentPreferences } from './state/preferences'
import './index.css'

// 暗色主题首屏同步生效（FR-92）：渲染前按持久化偏好打 .dark 类，避免浅→暗闪烁。
applyThemeToDocument(currentPreferences().theme)

// Monaco 自托管 + 中文化：从本地 /monaco/vs 加载（经 go:embed 随单二进制下发，离线可用、不依赖外网 CDN），
// 并启用中文 UI（查找框 / 右键菜单 / 快捷键面板）。资产与中文语言包由 scripts/vendor-monaco.mjs 生成、修正。
// 须在 Monaco 首次加载前配置才生效（base 固定为 '/'，与 vite.config 一致）。
loader.config({ paths: { vs: '/monaco/vs' }, 'vs/nls': { availableLanguages: { '*': 'zh-cn' } } })

// 假后端（mock）API 启用判定（无需真后端即可完整体验前端交互）：
// 1) 构建期 env 开关 VITE_USE_MOCK='true'（如 `pnpm dev:mock` 经 .env.mock 注入）→ 任何环境都启用；
// 2) 开发模式默认启用，除非显式 VITE_USE_MOCK='false' 关闭。
// 另：mock 模块自身还支持运行时 localStorage 开关（登录页「演示模式」触发），与本处构建期开关互不冲突。
import { enableMock } from './api/mock'
// import.meta.env 在本工程未纳入 tsconfig 的 .d.ts 类型（vite-env.d.ts 在 src 外），故按工程既有惯例做类型断言读取。
const viteEnv = (import.meta as unknown as { env: Record<string, string | boolean> }).env
const useMockEnv = viteEnv.VITE_USE_MOCK
if (useMockEnv === 'true' || (viteEnv.DEV && useMockEnv !== 'false')) {
  enableMock()
}

// React Query 客户端：管理台所有数据请求的缓存与状态来源。
// 全局错误兜底（FR-122/ADR-0057，见 .claude/rules/error-surfacing.md）：未自带 onError 的写操作失败也
// toast 出错误（message 为后端脱敏后的真实原因），杜绝静默失败；自带 onError 的 mutation 由其自行处理，避免重复 toast。
const queryClient = new QueryClient({
  mutationCache: new MutationCache({
    onError: (error, _vars, _ctx, mutation) => {
      if (mutation.options.onError) return
      const message = error instanceof Error ? error.message : ''
      toast.error(message || i18n.t('common.operationFailed'))
    },
  }),
})

// 应用入口：挂载 Router 与 QueryClient 两个 Provider，再渲染管理台空壳；
// Toaster 置于 Router 内、与 App 同级，登录页与受保护页均可弹出操作反馈。
const rootEl = document.getElementById('root')
if (!rootEl) {
  throw new Error(i18n.t('app.rootMissing'))
}

createRoot(rootEl).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
        <Toaster richColors closeButton />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
