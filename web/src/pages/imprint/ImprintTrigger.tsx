// 拓印触发面板（FR-46）：选在线服 + 文件 path → 触发拓印（命令 agent 读真实磁盘内容回传转存）。
// 触发即建 pending 命令；agent 回传后控制面转 ready，上层据命令状态轮询到 ready 再开 diff。
// 仅在线实例可作拓印源（离线 agent 收不到命令）。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery } from '@tanstack/react-query'

import { effectiveFiles, triggerImprint } from '../../api/client'
import type { AgentCommandView, InstanceView } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Label } from '@/components/ui/label'

export default function ImprintTrigger({
  instances,
  onTriggered,
}: {
  // 实例列表（仅在线实例可作拓印源）
  instances: InstanceView[]
  // 触发成功回调：把新建命令交上层开始轮询
  onTriggered: (cmd: AgentCommandView) => void
}) {
  const { t } = useTranslation()
  const msg = useMessage()
  // 拓印源：选中的在线实例 serverId
  const [serverId, setServerId] = useState('')
  // 目标文件相对 path（相对 plugins/）
  const [path, setPath] = useState('')

  const onlineInstances = useMemo(
    () => instances.filter((i) => i.status === 'online'),
    [instances],
  )

  // 源缺省取首个在线实例
  useEffect(() => {
    if (!serverId && onlineInstances.length > 0) {
      setServerId(onlineInstances[0].serverId)
    }
  }, [onlineInstances, serverId])

  // 源实例所属 namespace：实例列表跨 namespace，须随选中源实例带上。
  const sourceNamespace = useMemo(
    () => onlineInstances.find((i) => i.serverId === serverId)?.namespace ?? '',
    [onlineInstances, serverId],
  )

  // FR-69 候选文件清单：取所选源实例已知有效文件树的 path 列表，供关键字即时搜文件。
  // 候选随源实例变化一次性拉取，本地过滤即时筛（大树不卡，不每键打后端）；
  // 仅用 Beacon 已追踪/已解出的文件，不依赖反向抓取实时扫描（FR-58 P3 不在本批）。
  const filesQuery = useQuery({
    queryKey: ['imprint-candidate-files', sourceNamespace, serverId],
    queryFn: () => effectiveFiles({ namespace: sourceNamespace, serverId }),
    enabled: !!serverId && !!sourceNamespace,
  })

  // 候选 path：去重排序后喂给 combobox（候选缺失时回退空，combobox 仍可手输）。
  const pathOptions = useMemo(() => {
    const paths = (filesQuery.data?.files ?? []).map((f) => f.path)
    return Array.from(new Set(paths)).sort()
  }, [filesQuery.data])

  const triggerMut = useMutation({
    mutationFn: () => triggerImprint(serverId, sourceNamespace, { path: path.trim() }),
    onSuccess: (cmd) => {
      msg.showSuccess(t('imprint.msgTriggered'))
      onTriggered(cmd)
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onTrigger(e: React.FormEvent) {
    e.preventDefault()
    if (!serverId) {
      msg.showError(t('imprint.sourceRequired'))
      return
    }
    if (!path.trim()) {
      msg.showError(t('imprint.pathRequired'))
      return
    }
    triggerMut.mutate()
  }

  return (
    <form
      onSubmit={onTrigger}
      className="flex flex-wrap items-end gap-3 rounded-lg border border-border bg-card p-3"
    >
      <div className="space-y-1.5">
        <Label htmlFor="imp-source" className="text-xs">
          {t('imprint.sourceLabel')}
        </Label>
        <select
          id="imp-source"
          className="h-8 w-56 rounded border border-input bg-background px-2 text-sm"
          value={serverId}
          onChange={(e) => setServerId(e.target.value)}
        >
          <option value="">{t('common.pleaseSelect')}</option>
          {onlineInstances.map((i) => (
            <option key={i.serverId} value={i.serverId}>
              {i.serverId}（{i.group}）
            </option>
          ))}
        </select>
      </div>
      <div className="flex-1 min-w-[16rem] space-y-1.5">
        <Label htmlFor="imp-path" className="text-xs">
          {t('imprint.pathLabel')}
        </Label>
        {/* FR-69：关键字搜文件替代手输——allowCustom 可编辑模式既能键入即时筛候选、
            又能提交候选外的自定义 path（兜底拓印未追踪文件）。候选本地过滤，大树不卡。 */}
        <Combobox
          id="imp-path"
          aria-label={t('imprint.pathLabel')}
          className="font-mono"
          value={path}
          onChange={setPath}
          options={pathOptions}
          allowCustom
          placeholder={
            filesQuery.isLoading
              ? t('imprint.pathLoading')
              : t('imprint.pathSearchPlaceholder')
          }
        />
      </div>
      <Button type="submit" disabled={triggerMut.isPending || onlineInstances.length === 0}>
        {triggerMut.isPending ? t('imprint.triggering') : t('imprint.triggerBtn')}
      </Button>
      {/* 使用提示：搜索 + 兜底手输的上手说明 */}
      <p className="w-full text-xs text-muted-foreground">{t('imprint.usageHint')}</p>
      {/* 候选为空提示（仍可手输）：仅在已选源且候选拉取完成且确无已知文件时显示 */}
      {!!serverId && !filesQuery.isLoading && pathOptions.length === 0 && (
        <p className="w-full text-xs text-muted-foreground">{t('imprint.pathNoCandidate')}</p>
      )}
      {onlineInstances.length === 0 && (
        <p className="w-full text-xs text-muted-foreground">{t('imprint.noOnline')}</p>
      )}
    </form>
  )
}
