// 页面级异步分区：在 UI 包 AsyncSection 之上叠加「用户主动刷新」加载态。
//
// 为什么需要这一层：骨架原先只判 query.isPending，而页眉刷新走 invalidateQueries——
// 有缓存时 isPending 保持 false，点刷新页面毫无变化，用户以为没生效。
// 也不能直接用 useIsFetching：后台 8 秒轮询会让它长期为 true，页面会每 8 秒闪骨架。
// 故由页眉刷新按钮显式置位的全局信号（page-refreshing）驱动。
//
// 刷新时的呈现原则：**只有数据区变化，结构保持不动**。
// 整块骨架会把静态表头/卡片框架也重画一遍——这些本来就不变，重画不传递任何信息。
// 所以刷新态通过 context 下发给数据组件（如 DataTable 的 loading），
// 表头与页面结构原地保留，仅 <tbody> 换成骨架行。
import { createContext, useContext, type ReactNode, type ReactElement } from 'react'

import { AsyncSection } from '@beacon/ui'

import { usePageRefreshing } from '../../shell/page-refreshing'

/** 是否处于「用户主动刷新」中；数据组件据此只替换自身数据区。 */
export const RefreshLoadingContext = createContext(false)

/** 数据组件读取刷新态；未包裹在 PageAsyncSection 内时恒为 false。 */
export function useRefreshLoading(): boolean {
  return useContext(RefreshLoadingContext)
}

interface PageAsyncSectionProps {
  isLoading: boolean
  // 与 UI 包 AsyncSection 保持可选，便于只关心加载态的调用方省略错误 props
  isError?: boolean
  error?: unknown
  // 首屏加载骨架；不传时仅保留内容区（部分调用方自行处理）
  skeleton?: ReactElement
  children: ReactNode
  // 兼容既有调用方的签名：刷新态一律只让数据区 loading，不再整块替换，
  // 故该参数仅作接受用途，不再改变呈现方式。
  keepContentWhileRefreshing?: boolean
  // 与 UI 包 AsyncSection 同名的加载文案，透传使用
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

  // 刷新中：保留结构与已有内容，仅让数据区进入加载态。
  // 非表格内容（卡片、说明等）不额外替换——数据组件自己会读 context 处理。
  return (
    <RefreshLoadingContext.Provider value={refreshing}>
      <div aria-busy={refreshing ? true : undefined}>
        <AsyncSection
          isLoading={false}
          isError={isError}
          error={error}
          skeleton={skeleton}
          loadingText={loadingText}
        >
          {children}
        </AsyncSection>
      </div>
    </RefreshLoadingContext.Provider>
  )
}
