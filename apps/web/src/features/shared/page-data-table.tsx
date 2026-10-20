// 数据表格：UI 包 DataTable 的页面级别名。
//
// 历史上这一层负责把 web 侧的「用户主动刷新」信号翻译成 DataTable 的 loading
// （UI 包不能反向依赖 web 的 shell 信号，分层约束），从而做到「刷新时只有数据区
// loading、表头不动」。但该信号背后的 Provider 在 9887644d 被移除后，hook 恒为
// false，兜底从未生效——现已删除该死兜底，loading 完全由调用方显式传入决定。
//
// 仍保留独立模块而非让各页面直接 import DataTable：布局页统一沿这个路径引用，
// 后续若要重新引入页面级刷新态，只需改这一处。
import type { ComponentProps } from 'react'

import { DataTable } from '@beacon/ui'

type PageDataTableProps<T> = ComponentProps<typeof DataTable<T>>

export default function PageDataTable<T>(props: PageDataTableProps<T>) {
  return <DataTable<T> {...props} />
}
