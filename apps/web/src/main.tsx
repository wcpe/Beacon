import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import '@beacon/ui/styles.css'
import './styles.css'
import './i18n'
import AppShell from './shell/app-shell'
import { isDemoMode } from './demo-mode'

const queryClient = new QueryClient()

async function bootstrap() {
  // 仅演示模式注册 mock worker：常规发布产物不得拦截 /admin/*，否则真实控制面不可达。
  // 动态 import 使 @beacon/devmock 的 handlers 不进入生产包。
  if (isDemoMode()) {
    const { startControlPlaneMocking } = await import('@beacon/devmock')
    await startControlPlaneMocking()
  }
  const rootElement = document.getElementById('root')
  if (!rootElement) {
    throw new Error('缺少应用根节点')
  }

  ReactDOM.createRoot(rootElement).render(
    <React.StrictMode>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <AppShell />
        </BrowserRouter>
      </QueryClientProvider>
    </React.StrictMode>,
  )
}

void bootstrap()
