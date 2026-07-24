// env 展示维度响应契约（/admin/v2/envs*）。
// 契约真源：docs/specs/v2-zone-authority.md §3.4 / §4.1 / §5。
// env 是纯展示 / 过滤维度：不参与隔离判定、不参与调度、不进配置作用域链；
// env→namespace 映射为整体替换语义，一个 namespace 至多属于一个 env（冲突 409 指明冲突方）。

import type { Paged } from './common'

/** env 映射到的单个 namespace 摘要（id + 展示名，名取 namespace.code 与 /namespaces 列表口径一致） */
export interface EnvNamespaceRef {
  id: number
  name: string
}

/** env 列表项 / 单条视图：含映射的 namespace 摘要 */
export interface EnvItem {
  id: number
  name: string
  description: string
  namespaces: EnvNamespaceRef[]
  namespaceCount: number
  createdAt: string
  updatedAt: string
}

export type EnvListResponse = Paged<EnvItem>
