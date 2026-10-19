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
import type { ReactNode, ReactElement } from 'react'

import { AsyncSection } from '@beacon/ui'

import { usePageRefreshing } from '../../shell/page-refreshing'

/**
 * 兼容保留：刷新态不再统一干预数据组件（遮罩已移除，数值跳动与表格骨架由各组件自行表达）。
 * 保留该 hook 是为了避免各调用点大改；它现在恒为 false。
 */
export function useRefreshLoading(): boolean {
  return false
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

  // 刷新时不覆盖、不替换内容：数值更新由各数据组件的跳动/骨架自行表达，
  // 整块遮罩会盖住用户正在看的数据，反而碍事。
  return (
    <div aria-busy={refreshing ? true : undefined}>
      <AsyncSection isLoading={false} isError={isError} error={error} skeleton={skeleton} loadingText={loadingText}>
        {children}
      </AsyncSection>
    </div>
  )
}
