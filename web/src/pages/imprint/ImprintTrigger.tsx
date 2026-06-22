// 拓印触发面板（FR-46）：选在线服 + 文件 path → 触发拓印（命令 agent 读真实磁盘内容回传转存）。
// 触发即建 pending 命令；agent 回传后控制面转 ready，上层据命令状态轮询到 ready 再开 diff。
// 仅在线实例可作拓印源（离线 agent 收不到命令）。

import { useEffect, useMemo, useState } from 'react'
import { useMutation } from '@tanstack/react-query'

import { triggerImprint } from '../../api/client'
import type { AgentCommandView, InstanceView } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
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

  const triggerMut = useMutation({
    mutationFn: () => triggerImprint(serverId, sourceNamespace, { path: path.trim() }),
    onSuccess: (cmd) => {
      msg.showSuccess('已触发拓印，等待该实例回传磁盘内容…')
      onTriggered(cmd)
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onTrigger(e: React.FormEvent) {
    e.preventDefault()
    if (!serverId) {
      msg.showError('请选择在线实例作为拓印源')
      return
    }
    if (!path.trim()) {
      msg.showError('目标文件 path 为必填')
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
          拓印源（在线实例）
        </Label>
        <select
          id="imp-source"
          className="h-8 w-56 rounded border border-input bg-background px-2 text-sm"
          value={serverId}
          onChange={(e) => setServerId(e.target.value)}
        >
          <option value="">请选择</option>
          {onlineInstances.map((i) => (
            <option key={i.serverId} value={i.serverId}>
              {i.serverId}（{i.group}）
            </option>
          ))}
        </select>
      </div>
      <div className="flex-1 min-w-[16rem] space-y-1.5">
        <Label htmlFor="imp-path" className="text-xs">
          目标文件 path（相对 plugins/）
        </Label>
        <input
          id="imp-path"
          className="h-8 w-full rounded border border-input bg-background px-2 text-sm font-mono"
          value={path}
          onChange={(e) => setPath(e.target.value)}
          placeholder="如 AllinCore/config.yml"
        />
      </div>
      <Button type="submit" disabled={triggerMut.isPending || onlineInstances.length === 0}>
        {triggerMut.isPending ? '触发中…' : '拓印此文件'}
      </Button>
      {onlineInstances.length === 0 && (
        <p className="w-full text-xs text-muted-foreground">当前无在线实例可拓印</p>
      )}
    </form>
  )
}
