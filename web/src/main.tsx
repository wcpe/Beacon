import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import App from './App'

// React Query 客户端：管理台所有数据请求的缓存与状态来源
const queryClient = new QueryClient()

// 应用入口：挂载 Router 与 QueryClient 两个 Provider，再渲染管理台空壳
const rootEl = document.getElementById('root')
if (!rootEl) {
  throw new Error('找不到根节点 #root，无法挂载管理台')
}

createRoot(rootEl).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
