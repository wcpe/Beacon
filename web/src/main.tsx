import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider, MutationCache } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import { toast } from 'sonner'
import App from './App'
import { Toaster } from '@/components/ui/sonner'
// i18n 初始化（FR-50，见 ADR-0033）：import 即同步完成初始化，须在渲染前先执行
import i18n from './i18n'
import { applyThemeToDocument, currentPreferences } from './state/preferences'
import './index.css'

// 暗色主题首屏同步生效（FR-92）：渲染前按持久化偏好打 .dark 类，避免浅→暗闪烁。
applyThemeToDocument(currentPreferences().theme)

// 开发模式下启用 mock API（无需后端即可验证前端交互）
// 通过 VITE_USE_MOCK 环境变量控制，默认为 true（开发模式）
import { enableMock } from './api/mock'
if ((import.meta as unknown as { env: Record<string, string> }).env.DEV && (import.meta as unknown as { env: Record<string, string> }).env.VITE_USE_MOCK !== 'false') {
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
