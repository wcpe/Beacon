// 页面级异步分区：在 UI 包 AsyncSection 之上叠加「用户主动刷新」加载态。
//
// 为什么需要这一层：骨架原先只判 query.isPending，而页眉刷新走 invalidateQueries——
// 有缓存时 isPending 保持 false，点刷新页面毫无变化，用户以为没生效。
// 也不能直接用 useIsFetching：后台 8 秒轮询会让它长期为 true，页面会每 8 秒闪一次。
// 故由页眉刷新按钮显式置位的全局信号（page-refreshing）驱动。
//
// 刷新时的呈现：**保留已有数据，在数据区上盖一层半透明遮罩 + 居中转圈**。
// 不用骨架替换——结构（表头/卡片框架）本来就不变，重画不传递信息；
// 也不用数据行骨架——那会把用户正在看的行抽走。遮罩既表示「正在刷新」，
// 又不丢失上下文。
import { Loader2 } from 'lucide-react'
import { createContext, useContext, type ReactNode, type ReactElement } from 'react'

import { AsyncSection } from '@beacon/ui'

import { usePageRefreshing } from '../../shell/page-refreshing'

/**
 * 兼容保留：刷新态已改为「数据区遮罩 + 转圈」，由本组件统一覆盖，
 * 数据组件（DataTable / KPI 等）不再需要自行响应刷新态。
 * 保留该 hook 与 context 是为了避免各调用点大改；它现在恒为 false。
 */
const RefreshLoadingContext = createContext(false)

export function useRefreshLoading(): boolean {
  return useContext(RefreshLoadingContext)
}

interface PageAsyncSectionProps {
  isLoading: boolean
  isError?: boolean
  error?: unknown
  skeleton?: ReactElement
  children: ReactNode
  // 兼容既有调用方签名；刷新态一律走「遮罩 + 转圈」，该参数不再改变呈现。
  keepContentWhileRefreshing?: boolean
  loadingText?: ReactNode
}

export default function PageAsyncSection({
  isLoading,
  isError = false,
  error = null,
  skeleton,
  children,
  loadingText,
}: PageAsyncSectionProps) {
  const refreshing = usePageRefreshing()

  // 首屏加载：此时没有既有内容可保留，展示骨架是合理的
  if (isLoading) {
    return (
      <AsyncSection isLoading isError={false} error={null} skeleton={skeleton} loadingText={loadingText}>
        {children}
      </AsyncSection>
    )
  }

  return (
    <div className="relative" aria-busy={refreshing ? true : undefined}>
      <AsyncSection isLoading={false} isError={isError} error={error} skeleton={skeleton} loadingText={loadingText}>
        {children}
      </AsyncSection>
      {refreshing && (
        // 遮罩：覆盖数据区，半透明，保留底层数据可见但不抢注意力
        <div
          data-slot="refresh-overlay"
          className="absolute inset-0 z-10 grid place-items-center rounded-lg bg-background/55 backdrop-blur-[1px] motion-reduce:backdrop-blur-none"
        >
          <div className="flex items-center gap-2 rounded-full border border-border bg-card px-3 py-1.5 text-xs text-ink-2 shadow-card">
            <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden />
            {loadingText ?? '正在刷新'}
          </div>
        </div>
      )}
    </div>
  )
}
