// 按需拓印审核台（FR-46）：选在线服 + 文件 → 触发拓印 → 轮询命令至 ready →
// 展示 diff（期望合并值 ⟷ 本地实际值 + FR-45 逐键来源徽标）→ 选并入层 + 预览 → 单人自审确认同步。
// 改动必经此处人工确认（守架构不变量 #1，不退化为「控制面在服务器上执行」）：拓印只搬磁盘原文待审，
// 确认才落为某层文件覆盖、走通道B 既有下发。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'

import { imprintStatus, listInstances, zoneSummary } from '../api/client'
import type { AgentCommandView } from '../api/types'
import { usePageHeader } from '@/components/PageHeader'
import ImprintTrigger from './imprint/ImprintTrigger'
import ImprintDiffPanel from './imprint/ImprintDiffPanel'

export default function ImprintPage() {
  const { t } = useTranslation()
  // 当前待审拓印命令（触发后赋值，轮询其状态至 ready）
  const [command, setCommand] = useState<AgentCommandView | null>(null)

  const instancesQuery = useQuery({ queryKey: ['instances-all'], queryFn: () => listInstances({}) })
  const zonesQuery = useQuery({ queryKey: ['zones-summary'], queryFn: () => zoneSummary() })

  // 大区候选（zone 汇总 + 实例派生的并集，作为并入层目标组）
  const groupOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of zonesQuery.data ?? []) if (z.group) set.add(z.group)
    for (const i of instancesQuery.data ?? []) if (i.group) set.add(i.group)
    return Array.from(set).sort()
  }, [zonesQuery.data, instancesQuery.data])

  // 轮询命令状态至 ready（仅在已触发且尚未 ready 时拉，2 秒一次）。
  const statusQuery = useQuery({
    queryKey: ['imprint-status', command?.id],
    queryFn: () => imprintStatus(command!.id),
    enabled: !!command && command.status !== 'ready',
    refetchInterval: (q) => {
      const s = q.state.data?.status
      // 已就绪 / 终态则停止轮询
      if (s === 'ready' || s === 'done' || s === 'failed' || s === 'expired') return false
      return 2000
    },
  })

  // 有效命令状态：以轮询结果为准，回退到触发时的命令状态。
  const status = statusQuery.data?.status ?? command?.status
  // 拓印源所属大区（diff 面板并入层 group 缺省）
  const sourceGroup = useMemo(
    () =>
      (instancesQuery.data ?? []).find((i) => i.serverId === command?.serverId)?.group ?? '',
    [instancesQuery.data, command],
  )

  // 第二层页眉：标题 + 计数徽章；本页为环境范围页
  usePageHeader({
    title: t('imprint.title'),
    count: t('imprint.badge'),
    envScoped: true,
  })

  return (
    <div className="flex flex-col h-full overflow-hidden gap-2">
      <ImprintTrigger
        instances={instancesQuery.data ?? []}
        onTriggered={(cmd) => setCommand(cmd)}
      />

      {/* 命令态提示 / diff 面板 */}
      {!command ? (
        <div className="flex flex-1 items-center justify-center rounded-lg border border-dashed border-border text-sm text-muted-foreground">
          {t('imprint.emptyHint')}
        </div>
      ) : status === 'ready' ? (
        <div className="flex flex-1 min-h-0 rounded-lg border border-border bg-card overflow-hidden">
          <ImprintDiffPanel
            commandId={command.id}
            serverId={command.serverId}
            sourceGroup={sourceGroup}
            groups={groupOptions}
            // L：目标子服选择器仅限拓印源所属 namespace 的实例，避免跨 ns 选到悬空目标
            instances={(instancesQuery.data ?? []).filter((i) => i.namespace === command.namespace)}
            onConfirmed={() => setCommand(null)}
          />
        </div>
      ) : status === 'done' ? (
        <div className="flex flex-1 items-center justify-center rounded-lg border border-border text-sm text-muted-foreground">
          {t('imprint.done')}
        </div>
      ) : status === 'failed' || status === 'expired' ? (
        <div className="flex flex-1 items-center justify-center rounded-lg border border-border text-sm text-destructive">
          {status === 'failed' ? t('imprint.failed') : t('imprint.expired')}
        </div>
      ) : (
        <div className="flex flex-1 items-center justify-center rounded-lg border border-border text-sm text-muted-foreground">
          {t('imprint.waiting', { serverId: command.serverId, status })}
        </div>
      )}
    </div>
  )
}
