// 页面级异步分区：在 UI 包 AsyncSection 之上叠加「用户主动刷新」加载态。
//
// 为什么需要这一层：骨架原先只判 query.isPending，而页眉刷新走 invalidateQueries——
// 有缓存时 isPending 保持 false，点刷新页面毫无变化，用户以为没生效。
// 也不能直接用 useIsFetching：后台 8 秒轮询会让它长期为 true，页面会每 8 秒闪骨架。
// 故由页眉刷新按钮显式置位的全局信号（page-refreshing）驱动，覆盖在原有加载态之上。
import type { ReactNode } from 'react'

import { AsyncSection } from '@beacon/ui'

import { usePageRefreshing } from '../../shell/page-refreshing'

interface PageAsyncSectionProps {
  isLoading: boolean
  isError?: boolean
  error?: unknown
  loadingText?: ReactNode
  skeleton?: ReactNode
  children: ReactNode
  // 刷新中是否保留当前内容并叠加淡出（默认 false）；置 true 可避免详情或表单内容闪烁。
  keepContentWhileRefreshing?: boolean
}

export default function PageAsyncSection({
  isLoading,
  isError = false,
  error,
  loadingText,
  skeleton,
  children,
  keepContentWhileRefreshing = false,
}: PageAsyncSectionProps) {
  const refreshing = usePageRefreshing()

  // 首屏加载：展示骨架（原行为）
  if (isLoading) {
    return (
      <AsyncSection isLoading isError={false} error={null} loadingText={loadingText} skeleton={skeleton}>
        {children}
      </AsyncSection>
    )
  }

  // 刷新中：按需叠加加载态
  if (refreshing) {
    if (keepContentWhileRefreshing) {
      return (
        <div className="transition-opacity duration-200 motion-reduce:transition-none" aria-busy="true">
          <div className="opacity-60">{children}</div>
        </div>
      )
    }
    return (
      <div aria-busy="true">
        <AsyncSection isLoading isError={false} error={null} loadingText={loadingText} skeleton={skeleton}>
          {children}
        </AsyncSection>
      </div>
    )
  }

  return (
    <AsyncSection isLoading={false} isError={isError} error={error} loadingText={loadingText} skeleton={skeleton}>
      {children}
    </AsyncSection>
  )
}
