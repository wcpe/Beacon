// 反向抓取扫描任务触发面板（FR-60）：选在线 bukkit 源 + 入库目标层（group/server）+ 目标组 + 目标子服 →
// createScanTask（两段式第一段：命令 agent 扫描其 plugins/ 树回传清单）。复用 ImprintTrigger 的在线源选择范式。
// 仅在线实例可作抓取源（离线 agent 收不到命令）。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation } from '@tanstack/react-query'

import { createScanTask } from '../../api/client'
import type { InstanceView, ReverseFetchScope, ReverseFetchTaskView } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'

export default function ReverseFetchTaskTrigger({
  instances,
  groups,
  onCreated,
}: {
  // 实例列表（仅在线 bukkit 实例可作抓取源）
  instances: InstanceView[]
  // 大区候选（入库目标组）
  groups: string[]
  // 建任务成功回调：把新建任务交上层开始轮询
  onCreated: (task: ReverseFetchTaskView) => void
}) {
  const { t } = useTranslation()
  const msg = useMessage()
  // 抓取源：选中的在线 bukkit 实例 serverId
  const [serverId, setServerId] = useState('')
  // 入库目标层（group 落组级 / server 落实例级）
  const [scope, setScope] = useState<ReverseFetchScope>('group')
  // 目标大区
  const [group, setGroup] = useState('')
  // 仅 server 层需要：覆盖落到哪个目标子服
  const [target, setTarget] = useState('')

  // 仅在线 bukkit 可作源（bungee 无 plugins 配置树可抓）
  const onlineSources = useMemo(
    () => instances.filter((i) => i.status === 'online' && i.role === 'bukkit'),
    [instances],
  )

  // 源缺省取首个在线源；并据其 group 缺省入库目标组（同组回写最常见）
  useEffect(() => {
    if (!serverId && onlineSources.length > 0) {
      const first = onlineSources[0]
      setServerId(first.serverId)
      if (!group) setGroup(first.group)
    }
  }, [onlineSources, serverId, group])

  // 源实例所属 namespace：实例列表跨 namespace，须随选中源实例带上。
  const sourceNamespace = useMemo(
    () => onlineSources.find((i) => i.serverId === serverId)?.namespace ?? '',
    [onlineSources, serverId],
  )

  // server 层目标子服候选：仅同 namespace 的实例，避免跨 ns 选到悬空目标。
  const targetOptions = useMemo(
    () => instances.filter((i) => i.namespace === sourceNamespace),
    [instances, sourceNamespace],
  )

  const createMut = useMutation({
    mutationFn: () =>
      createScanTask(serverId, sourceNamespace, {
        scope,
        group: group.trim(),
        target: scope === 'server' ? target : undefined,
      }),
    onSuccess: (task) => {
      msg.showSuccess(t('reverseFetchTask.msgCreated'))
      onCreated(task)
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!serverId) {
      msg.showError(t('reverseFetchTask.sourceRequired'))
      return
    }
    if (!group.trim()) {
      msg.showError(t('reverseFetchTask.groupRequired'))
      return
    }
    if (scope === 'server' && !target) {
      msg.showError(t('reverseFetchTask.targetRequired'))
      return
    }
    createMut.mutate()
  }

  return (
    <form
      onSubmit={onSubmit}
      className="flex flex-wrap items-end gap-3 rounded-lg border border-border bg-card p-3"
    >
      <div className="space-y-1.5">
        <Label htmlFor="rft-source" className="text-xs">
          {t('reverseFetchTask.sourceLabel')}
        </Label>
        <select
          id="rft-source"
          className="h-8 w-56 rounded border border-input bg-background px-2 text-sm"
          value={serverId}
          onChange={(e) => setServerId(e.target.value)}
        >
          <option value="">{t('common.pleaseSelect')}</option>
          {onlineSources.map((i) => (
            <option key={i.serverId} value={i.serverId}>
              {i.serverId}（{i.group}）
            </option>
          ))}
        </select>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="rft-scope" className="text-xs">
          {t('reverseFetchTask.scopeLabel')}
        </Label>
        <select
          id="rft-scope"
          className="h-8 w-32 rounded border border-input bg-background px-2 text-sm"
          value={scope}
          onChange={(e) => setScope(e.target.value as ReverseFetchScope)}
        >
          <option value="group">{t('reverseFetchTask.scopeGroup')}</option>
          <option value="server">{t('reverseFetchTask.scopeServer')}</option>
        </select>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="rft-group" className="text-xs">
          {t('reverseFetchTask.groupLabel')}
        </Label>
        <select
          id="rft-group"
          className="h-8 w-36 rounded border border-input bg-background px-2 text-sm"
          value={group}
          onChange={(e) => setGroup(e.target.value)}
        >
          <option value="">{t('common.pleaseSelect')}</option>
          {groups.map((g) => (
            <option key={g} value={g}>
              {g}
            </option>
          ))}
        </select>
      </div>
      {scope === 'server' && (
        <div className="space-y-1.5">
          <Label htmlFor="rft-target" className="text-xs">
            {t('reverseFetchTask.targetLabel')}
          </Label>
          <select
            id="rft-target"
            className="h-8 w-44 rounded border border-input bg-background px-2 text-sm"
            value={target}
            onChange={(e) => setTarget(e.target.value)}
          >
            <option value="">{t('common.pleaseSelect')}</option>
            {targetOptions.map((i) => (
              <option key={i.serverId} value={i.serverId}>
                {i.serverId}（{i.group}）
              </option>
            ))}
          </select>
        </div>
      )}
      <Button type="submit" disabled={createMut.isPending || onlineSources.length === 0}>
        {createMut.isPending ? t('reverseFetchTask.creating') : t('reverseFetchTask.createBtn')}
      </Button>
      <p className="w-full text-xs text-muted-foreground">{t('reverseFetchTask.usageHint')}</p>
      {onlineSources.length === 0 && (
        <p className="w-full text-xs text-muted-foreground">{t('reverseFetchTask.noOnline')}</p>
      )}
    </form>
  )
}
