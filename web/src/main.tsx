import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { Toaster } from '@/components/ui/sonner'
import './index.css'

// 开发模式下启用 mock API（无需后端即可验证前端交互）
// 通过 VITE_USE_MOCK 环境变量控制，默认为 true（开发模式）
import { enableMock } from './api/mock'
if ((import.meta as unknown as { env: Record<string, string> }).env.DEV && (import.meta as unknown as { env: Record<string, string> }).env.VITE_USE_MOCK !== 'false') {
  enableMock()
}

// React Query 客户端：管理台所有数据请求的缓存与状态来源
const queryClient = new QueryClient()

// 应用入口：挂载 Router 与 QueryClient 两个 Provider，再渲染管理台空壳；
// Toaster 置于 Router 内、与 App 同级，登录页与受保护页均可弹出操作反馈。
const rootEl = document.getElementById('root')
if (!rootEl) {
  throw new Error('找不到根节点 #root，无法挂载管理台')
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
