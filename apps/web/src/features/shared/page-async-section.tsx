// 页面级异步分区：在 UI 包 AsyncSection 之上叠加「用户主动刷新」加载态。
//
// 为什么需要这一层：骨架原先只判 query.isPending，而页眉刷新走 invalidateQueries——
// 有缓存时 isPending 保持 false，点刷新页面毫无变化，用户以为没生效。
// 也不能直接用 useIsFetching：后台 8 秒轮询会让它长期为 true，页面会每 8 秒闪一次。
// 故由页眉刷新按钮显式置位的全局信号（page-refreshing）驱动。
//
// 刷新时的呈现：**保留已有数据，只在容器上标记 aria-busy**。
// 不替换骨架——结构（表头/卡片框架）本来就不变，重画不传递信息；
// 也不额外盖遮罩——那会挡住用户正在看的数据。刷新带来的数值变化由
// KpiCard 的 JumpValue 跳动表达，读屏反馈由 aria-busy 承担。
//
// 历史说明：此处曾导出 useRefreshLoading()（供各数据组件判定「正在刷新」），
// 但它在 9887644d 随 Provider 一起被移除后恒返回 false，6 处调用点因此全是死分支。
// 现已删除该导出及其调用面——不保留「看起来还能工作」的空壳。
import type { ReactNode, ReactElement } from 'react'

import { AsyncSection } from '@beacon/ui'

import { usePageRefreshing } from '../../shell/page-refreshing'

interface PageAsyncSectionProps {
  isLoading: boolean
  isError?: boolean
  error?: unknown
  skeleton?: ReactElement
  children: ReactNode
  // 兼容既有调用方签名：本组件始终保留已有内容，该参数不再改变呈现。
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

  // 刷新时不覆盖、不替换内容：只在容器上标记 aria-busy 供读屏，
  // 数值变化由 KpiCard 的 JumpValue 跳动表达；整块遮罩/骨架会盖住用户正在看的数据。
  return (
    <div aria-busy={refreshing ? true : undefined}>
      <AsyncSection isLoading={false} isError={isError} error={error} skeleton={skeleton} loadingText={loadingText}>
        {children}
      </AsyncSection>
    </div>
  )
}
