// 两层页眉之第二层「页面头带」PageHeader（FR-105）：槽位固定、单一真源。
// 左：标题 + 计数/副标题槽；右：环境槽（仅环境范围页）+ 页面主操作槽。
//
// 协作模型（用模块级外部 store，镜像 state/preferences.ts 范式）：
//   - usePageHeader(config)：各页在渲染期把自身页头配置写入模块 store（effect 同步、卸载清空）。
//   - PageHeader（Layout 渲染）：useSyncExternalStore 订阅 store + 当前路由，渲染第二层。
// 为何不用 Context 持有 state：若 Provider 用 useState 持有配置，setConfig 会重渲染其子树（含页面），
//   而页面每次渲染都产出新的 actions/title JSX 节点，触发 effect 再 setConfig，形成无限更新环。
//   改用外部 store：写配置只重渲染订阅它的 PageHeader，不回灌页面子树，从根上消除该环。
//   PageHeaderProvider 保留为透明包裹以稳定对外 API（Layout 仍包裹内容列）。

import { useEffect, useRef, useSyncExternalStore, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import EnvSelector from '@/components/EnvSelector'
import { NAV_LEAVES } from '@/lib/navModel'

// 各页注入的页头配置：标题（已渲染节点，通常为 t('xxx.title')）、计数/副标题、主操作、是否环境范围。
export interface PageHeaderConfig {
  // 页面标题（已解析的 ReactNode，一般传 t('xxx.title')）；未设则回退当前路由叶子的 labelKey
  title?: ReactNode
  // 副标题（小号弱色，标题右侧），用于承接原标题行的说明文字
  subtitle?: ReactNode
  // 计数（小号弱色，标题右侧），用于承接原标题行的计数徽章
  count?: ReactNode
  // 页面主操作槽（右侧），承接原标题行内的主操作按钮
  actions?: ReactNode
  // 是否环境范围页（覆盖路由叶子的默认标记）：true 时右侧渲染环境选择器
  envScoped?: boolean
}

// ===== 模块级外部 store（订阅者模式）=====
// 当前页头配置快照（默认空）
let snapshot: PageHeaderConfig = {}
// 配置的当前归属者令牌：用于切页时避免「旧页卸载清空」误清「新页已写入的配置」。
let ownerToken = 0
// 订阅者集合：配置变化时通知 PageHeader 重渲染
const listeners = new Set<() => void>()

function subscribe(cb: () => void): () => void {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}

function getSnapshot(): PageHeaderConfig {
  return snapshot
}

// 写入配置并广播（仅重渲染订阅 store 的 PageHeader，不触及页面子树）。返回本次写入的归属令牌。
function setConfig(next: PageHeaderConfig): number {
  snapshot = next
  ownerToken += 1
  for (const l of listeners) l()
  return ownerToken
}

// 清空配置，但仅当当前归属仍属调用方时才清（token 匹配）：
// 切页时新页 mount 先写入并夺得归属，旧页卸载清空时 token 已不匹配，遂跳过，避免清掉新页头。
function clearConfigIfOwner(token: number): void {
  if (ownerToken !== token) return
  snapshot = {}
  ownerToken += 1
  for (const l of listeners) l()
}

// PageHeaderProvider：透明包裹（稳定对外 API）。配置真源已迁至模块 store，故此处不持有 state。
export function PageHeaderProvider({ children }: { children: ReactNode }) {
  return <>{children}</>
}

// usePageHeader：各页在组件内调用，把页头配置同步进模块 store（依赖变化时更新、卸载时按归属清空）。
export function usePageHeader(config: PageHeaderConfig): void {
  const { title, subtitle, count, actions, envScoped } = config
  // 记录本组件最近一次写入的归属令牌，卸载时据此判断是否仍由本组件归属
  const tokenRef = useRef(0)
  useEffect(() => {
    tokenRef.current = setConfig({ title, subtitle, count, actions, envScoped })
    return () => clearConfigIfOwner(tokenRef.current)
    // 各字段按值依赖触发更新；actions/title 等 JSX 节点每次渲染为新对象，
    // 但写 store 不回灌页面子树，故不会形成更新环。
  }, [title, subtitle, count, actions, envScoped])
}

// PageHeader：第二层页面头带，由 Layout 渲染。订阅 store + 路由，组装标题 + 计数/副标题 + 环境槽 + 主操作槽。
export default function PageHeader() {
  const { t } = useTranslation()
  const location = useLocation()
  // 订阅模块 store：页头配置变化时重渲染（仅本组件）
  const config = useSyncExternalStore(subscribe, getSnapshot, getSnapshot)

  // 当前路由匹配的导航叶子：用于标题回退与默认环境范围标记。
  const leaf = NAV_LEAVES.find((l) => l.to === location.pathname)

  // 标题：页设了用页设值，否则回退当前路由叶子 labelKey；都无则空。
  const title = config.title ?? (leaf ? t(leaf.labelKey) : null)

  // 是否环境范围：页 config 显式优先，否则取路由叶子标记，缺省 false。
  const envScoped = config.envScoped ?? leaf?.envScoped ?? false

  return (
    // 第二层页面头带（FR-105 真机打磨：高度压低至 ~40px）。
    // fix-B：min-w-0 + overflow-hidden，标题 shrink-0 不换行（防窄屏被挤成竖排字符）；副标题次要、窄屏隐藏。
    <div className="flex h-10 min-w-0 shrink-0 items-center gap-3 overflow-hidden border-b bg-background px-6 py-2">
      {/* 左：标题 + 计数/副标题（小号弱色） */}
      <h1 className="shrink-0 text-sm font-semibold whitespace-nowrap">{title}</h1>
      {config.count != null && (
        <span className="shrink-0 text-sm text-muted-foreground">{config.count}</span>
      )}
      {config.subtitle != null && (
        <span className="hidden min-w-0 text-sm text-muted-foreground lg:flex">{config.subtitle}</span>
      )}
      {/* 右：环境槽（仅环境范围页）+ 主操作槽（不收缩，保持可点） */}
      <div className="ml-auto flex shrink-0 items-center gap-2">
        {envScoped && <EnvSelector />}
        {config.actions}
      </div>
    </div>
  )
}
