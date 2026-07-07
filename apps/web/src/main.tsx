import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import { startControlPlaneMocking } from '@beacon/devmock'
import '@beacon/ui/styles.css'
import './styles.css'
import './i18n'
import AppShell from './App'

const queryClient = new QueryClient()

async function bootstrap() {
  await startControlPlaneMocking()
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
