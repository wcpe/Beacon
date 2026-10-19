// 数据表格：在 UI 包 DataTable 之上接入「主动刷新」加载态。
//
// UI 包不能反向依赖 web 的 shell 信号（分层约束），故在此处读 context 后
// 以 props 形式把 loading 传给 DataTable。页面把 <DataTable> 换成 <PageDataTable>
// 即可获得「刷新时只有数据区 loading、表头不动」的行为。
import type { ComponentProps } from 'react'

import { DataTable } from '@beacon/ui'

import { useRefreshLoading } from './page-async-section'

type PageDataTableProps<T> = ComponentProps<typeof DataTable<T>>

export default function PageDataTable<T>(props: PageDataTableProps<T>) {
  const refreshing = useRefreshLoading()
  // loading 显式传入时以调用方为准（例如首屏以外的自定义场景）
  return <DataTable<T> {...props} loading={props.loading ?? refreshing} />
}
