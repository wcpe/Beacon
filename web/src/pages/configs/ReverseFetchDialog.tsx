// 从在线实例反向抓取对话框（FR-39）：命令某台在线实例读其真实 plugins/ 文本配置回传，
// 由控制面 ingest 为组级 / 实例级文件树覆盖（反向，对应 FR-38 正向「导入到组」）。
// 触发即创建 pending 命令并返回，后续结果经命令状态 / 审计 / 文件树体现；本对话框只负责触发与提示。

import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { triggerReverseFetch } from '../../api/client'
import type { ReverseFetchScope } from '../../api/types'
import type { InstanceView } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'

export default function ReverseFetchDialog({
  instances,
  groups,
}: {
  // 实例列表（来自 listInstances）；仅在线实例可作抓取源
  instances: InstanceView[]
  // 大区列表（由 zone 汇总 / 实例派生），作为入库目标组候选
  groups: string[]
}) {
  const qc = useQueryClient()
  const msg = useMessage()
  const [open, setOpen] = useState(false)
  // 抓取源：选中的在线实例 serverId
  const [serverId, setServerId] = useState('')
  // 目标层：group（落组级覆盖）/ server（落实例级覆盖）
  const [scope, setScope] = useState<ReverseFetchScope>('group')
  // 入库目标组
  const [group, setGroup] = useState('')
  // 仅 server 层：覆盖落到哪个 serverId
  const [target, setTarget] = useState('')

  // 仅在线实例可作抓取源（lost / offline 的 agent 收不到命令）
  const onlineInstances = useMemo(
    () => instances.filter((i) => i.status === 'online'),
    [instances],
  )

  // 打开时重置选择：源取首个在线实例，目标层缺省组级，组与目标实例清空待选。
  useEffect(() => {
    if (open) {
      setServerId(onlineInstances[0]?.serverId ?? '')
      setScope('group')
      setGroup('')
      setTarget('')
    }
  }, [open, onlineInstances])

  // 源实例所属 namespace：实例列表跨 namespace，须随选中的源实例带上（后端按 ?namespace= 定位）。
  const sourceNamespace = useMemo(
    () => onlineInstances.find((i) => i.serverId === serverId)?.namespace ?? '',
    [onlineInstances, serverId],
  )

  const fetchMut = useMutation({
    mutationFn: () =>
      triggerReverseFetch(serverId, sourceNamespace, {
        scope,
        group,
        // 组级抓取无目标实例，仅 server 层携带 target
        target: scope === 'server' ? target : undefined,
      }),
    onSuccess: () => {
      msg.showSuccess(`已触发反向抓取（命令已下发，结果经审计与文件树体现）`)
      setOpen(false)
      // 失效文件相关缓存：ingest 完成后文件树会变更
      qc.invalidateQueries({ queryKey: ['files'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onTrigger(e: React.FormEvent) {
    e.preventDefault()
    if (!serverId) {
      msg.showError('请选择在线实例作为抓取源')
      return
    }
    if (!group) {
      msg.showError('目标组为必填')
      return
    }
    if (scope === 'server' && !target) {
      msg.showError('实例层需指定目标实例')
      return
    }
    fetchMut.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          反向抓取
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>从在线实例反向抓取</DialogTitle>
        </DialogHeader>
        <form id="reverse-fetch" onSubmit={onTrigger} className="grid grid-cols-2 gap-3">
          <div className="col-span-2 space-y-1.5">
            <Label htmlFor="rf-source">抓取源（在线实例）</Label>
            <select
              id="rf-source"
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
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
            {onlineInstances.length === 0 && (
              <p className="text-xs text-muted-foreground">当前无在线实例可抓取</p>
            )}
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="rf-scope">目标层</Label>
            <select
              id="rf-scope"
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={scope}
              onChange={(e) => setScope(e.target.value as ReverseFetchScope)}
            >
              <option value="group">组级</option>
              <option value="server">实例级</option>
            </select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="rf-group">目标组</Label>
            <select
              id="rf-group"
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={group}
              onChange={(e) => setGroup(e.target.value)}
            >
              <option value="">请选择</option>
              {groups.map((g) => (
                <option key={g} value={g}>
                  {g}
                </option>
              ))}
            </select>
          </div>
          {scope === 'server' && (
            <div className="col-span-2 space-y-1.5">
              <Label htmlFor="rf-target">目标实例</Label>
              <select
                id="rf-target"
                className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
              >
                <option value="">请选择</option>
                {instances.map((i) => (
                  <option key={i.serverId} value={i.serverId}>
                    {i.serverId}（{i.group}）
                  </option>
                ))}
              </select>
            </div>
          )}
          <p className="col-span-2 text-xs text-muted-foreground">
            将读取该实例真实 plugins/ 的文本配置并入库为所选层覆盖；不含 .jar 与二进制，受单文件 /
            总量 / 文件数上限约束。
          </p>
        </form>
        <DialogFooter>
          <Button type="submit" form="reverse-fetch" disabled={fetchMut.isPending}>
            {fetchMut.isPending ? '触发中…' : '触发抓取'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
