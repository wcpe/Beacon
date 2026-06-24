// 命令面板纯逻辑层（FR-83）：把「导航 / 配置·文件 / 服务器 / 审计动作」原始数据
// 归一成统一的 CommandItem，并提供子串过滤与分组——无副作用、可穷举单测。
// 组件层只管唤起 / 键盘导航 / 跳转，搜索逻辑全在这里。

import type { ConfigView, FileView, InstanceView } from '@/api/types'

// 命令项分组：决定渲染分区与空 query 时是否默认展示。
export type CommandGroup = 'navigation' | 'config' | 'server' | 'audit'

// 统一命令项：title/subtitle 参与过滤匹配，to 为回车跳转目标（含深链查询参数）。
export interface CommandItem {
  // 组内稳定唯一 id（用于 React key 与选中定位）
  id: string
  group: CommandGroup
  // 主标题（导航为页名、配置为 dataId、服务器为 serverId、审计为动作名）
  title: string
  // 副标题（可选：环境 / 路径 / 角色等上下文，一并参与匹配）
  subtitle?: string
  // 回车跳转目标路由
  to: string
}

// 导航项（路由 + 已译页名）：始终展示，空 query 也在。
export interface NavSource {
  to: string
  label: string
}

// 审计动作快捷项（动作 key + 已译动作名）：跳审计页并带 action 过滤。
export interface AuditActionSource {
  action: string
  label: string
}

// buildItems 的原始数据入参：导航 / 审计动作为静态；配置 / 文件 / 实例为面板打开时拉取，缺省空。
export interface BuildSources {
  navItems: NavSource[]
  auditActions: AuditActionSource[]
  configs?: ConfigView[]
  files?: FileView[]
  instances?: InstanceView[]
}

// 把原始数据归一成扁平 CommandItem 列表（顺序：导航 → 配置 → 文件 → 服务器 → 审计动作）。
export function buildItems(sources: BuildSources): CommandItem[] {
  const items: CommandItem[] = []

  // 导航：跳对应路由
  for (const nav of sources.navItems) {
    items.push({ id: `nav:${nav.to}`, group: 'navigation', title: nav.label, to: nav.to })
  }

  // 配置：按 dataId 命中，跳配置中心并带 dataId 深链
  for (const cfg of sources.configs ?? []) {
    items.push({
      id: `config:${cfg.id}`,
      group: 'config',
      title: cfg.dataId,
      subtitle: `${cfg.namespace} / ${cfg.group}`,
      to: `/configs?dataId=${encodeURIComponent(cfg.dataId)}`,
    })
  }

  // 文件：按 path 命中，同样落配置中心
  for (const file of sources.files ?? []) {
    items.push({
      id: `file:${file.id}`,
      group: 'config',
      title: file.path,
      subtitle: `${file.namespace} / ${file.group}`,
      to: `/configs?path=${encodeURIComponent(file.path)}`,
    })
  }

  // 服务器：按 serverId 命中，跳服务器页并带 serverId 深链
  for (const inst of sources.instances ?? []) {
    items.push({
      id: `server:${inst.serverId}`,
      group: 'server',
      title: inst.serverId,
      subtitle: `${inst.namespace} / ${inst.role}`,
      to: `/servers?serverId=${encodeURIComponent(inst.serverId)}`,
    })
  }

  // 审计动作：跳审计页并带 action 过滤
  for (const audit of sources.auditActions) {
    items.push({
      id: `audit:${audit.action}`,
      group: 'audit',
      title: audit.label,
      subtitle: audit.action,
      to: `/audits?action=${encodeURIComponent(audit.action)}`,
    })
  }

  return items
}

// 按 query 子串过滤（大小写无关，匹配 title + subtitle）。
// 空 query（去空白后为空）时只保留导航与审计动作——避免一打开就刷出全量配置 / 服务器噪声。
export function filterItems(items: CommandItem[], query: string): CommandItem[] {
  const q = query.trim().toLowerCase()
  if (q === '') {
    return items.filter((it) => it.group === 'navigation' || it.group === 'audit')
  }
  return items.filter((it) => {
    const hay = `${it.title} ${it.subtitle ?? ''}`.toLowerCase()
    return hay.includes(q)
  })
}

// 分组渲染用：按固定组顺序归类，组内保持传入顺序，空组不返回。
const GROUP_ORDER: CommandGroup[] = ['navigation', 'config', 'server', 'audit']

export interface GroupedItems {
  group: CommandGroup
  items: CommandItem[]
}

export function groupItems(items: CommandItem[]): GroupedItems[] {
  const result: GroupedItems[] = []
  for (const group of GROUP_ORDER) {
    const inGroup = items.filter((it) => it.group === group)
    if (inGroup.length > 0) {
      result.push({ group, items: inGroup })
    }
  }
  return result
}
