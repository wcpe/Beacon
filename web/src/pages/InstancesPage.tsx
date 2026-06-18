// 实例与健康页：按 namespace/group/zone/role/status 过滤，5 秒轮询健康。
// online/lost/offline 三色区分；未分配 zone 的行高亮；点行看只读详情；支持手动下线。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { listInstances, offlineInstance } from '../api/client'
import type { InstanceFilter } from '../api/client'
import type { InstanceView } from '../api/types'
import { formatTime } from '../api/format'
import StatusBadge from '../components/StatusBadge'
import { useMessage } from '../components/useMessage'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'

// 健康轮询周期（毫秒）
const REFETCH_MS = 5000

// Radix Select 不允许空串值，"全部"用哨兵值 all 表示，提交时转 undefined
const ALL = 'all'

export default function InstancesPage() {
  const qc = useQueryClient()
  const msg = useMessage()

  const [namespace, setNamespace] = useState('')
  const [group, setGroup] = useState('')
  const [zone, setZone] = useState('')
  const [role, setRole] = useState(ALL)
  const [status, setStatus] = useState(ALL)
  const [filter, setFilter] = useState<InstanceFilter>({})

  // 只读详情 Dialog 选中的实例（null 表示关闭）
  const [selectedInstance, setSelectedInstance] = useState<InstanceView | null>(null)

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['instances', filter],
    queryFn: () => listInstances(filter),
    refetchInterval: REFETCH_MS,
  })

  const offlineMut = useMutation({
    mutationFn: (serverId: string) => {
      const ns = filter.namespace
      if (!ns) throw new Error('下线需要先在过滤条件中指定环境')
      return offlineInstance(serverId, ns)
    },
    onSuccess: (_data, serverId) => {
      msg.showSuccess(`已下线实例 ${serverId}`)
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: namespace.trim() || undefined,
      group: group.trim() || undefined,
      zone: zone.trim() || undefined,
      role: role === ALL ? undefined : role,
      status: status === ALL ? undefined : status,
    })
  }

  // 二次确认通过后执行下线：先校验环境（下线接口需要 namespace），再触发 mutation
  function onConfirmOffline(serverId: string) {
    if (!filter.namespace) {
      msg.showError('请先在过滤条件中指定环境（下线接口需要 namespace）')
      return
    }
    offlineMut.mutate(serverId)
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">实例与健康</h1>
        {isFetching && <span className="text-sm text-muted-foreground">（刷新中…）</span>}
      </div>

      <Card>
        <CardContent className="space-y-3">
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="f-namespace">环境</Label>
              <Input id="f-namespace" value={namespace} onChange={(e) => setNamespace(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-group">大区</Label>
              <Input id="f-group" value={group} onChange={(e) => setGroup(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-zone">小区</Label>
              <Input id="f-zone" value={zone} onChange={(e) => setZone(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label>角色</Label>
              <Select value={role} onValueChange={setRole}>
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>全部</SelectItem>
                  <SelectItem value="bukkit">bukkit</SelectItem>
                  <SelectItem value="bungee">bungee</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>状态</Label>
              <Select value={status} onValueChange={setStatus}>
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>全部</SelectItem>
                  <SelectItem value="online">online</SelectItem>
                  <SelectItem value="lost">lost</SelectItem>
                  <SelectItem value="offline">offline</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button type="submit">查询</Button>
          </form>
          <p className="text-sm text-muted-foreground">
            提示：手动下线需在上方过滤条件中指定「环境」。未分配小区的实例以黄色高亮，点击行查看详情。
          </p>
        </CardContent>
      </Card>

      {isError && (
        <p className="text-sm text-destructive">加载失败：{(error as Error).message}</p>
      )}

      <Card>
        <CardContent>
          {isLoading ? (
            <p className="text-sm text-muted-foreground">加载中…</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>serverId</TableHead>
                  <TableHead>环境</TableHead>
                  <TableHead>角色</TableHead>
                  <TableHead>大区</TableHead>
                  <TableHead>小区</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>地址</TableHead>
                  <TableHead>版本</TableHead>
                  <TableHead>人数</TableHead>
                  <TableHead>TPS</TableHead>
                  <TableHead>最近心跳</TableHead>
                  <TableHead>操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data && data.length > 0 ? (
                  data.map((i) => (
                    <TableRow
                      key={`${i.namespace}/${i.serverId}`}
                      className={cn('cursor-pointer', !i.assigned && 'bg-amber-50')}
                      onClick={() => setSelectedInstance(i)}
                    >
                      <TableCell className="font-mono">{i.serverId}</TableCell>
                      <TableCell>{i.namespace}</TableCell>
                      <TableCell>{i.role}</TableCell>
                      <TableCell>{i.group}</TableCell>
                      <TableCell>
                        {i.zone === null ? (
                          <Badge variant="outline" className="border-amber-500 text-amber-600">
                            未分配
                          </Badge>
                        ) : (
                          i.zone
                        )}
                      </TableCell>
                      <TableCell>
                        <StatusBadge status={i.status} />
                      </TableCell>
                      <TableCell className="font-mono">{i.address}</TableCell>
                      <TableCell>{i.version}</TableCell>
                      <TableCell>{i.playerCount}</TableCell>
                      <TableCell>{i.tps.toFixed(1)}</TableCell>
                      <TableCell>{formatTime(i.lastHeartbeat)}</TableCell>
                      <TableCell>
                        <AlertDialog>
                          <AlertDialogTrigger asChild>
                            <Button
                              variant="destructive"
                              size="sm"
                              disabled={offlineMut.isPending}
                              onClick={(e) => e.stopPropagation()}
                            >
                              下线
                            </Button>
                          </AlertDialogTrigger>
                          <AlertDialogContent onClick={(e) => e.stopPropagation()}>
                            <AlertDialogHeader>
                              <AlertDialogTitle>确认下线实例 {i.serverId}？</AlertDialogTitle>
                              <AlertDialogDescription>
                                将把该实例标记为下线，下线接口需要过滤条件中指定的「环境」。
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>取消</AlertDialogCancel>
                              <AlertDialogAction onClick={() => onConfirmOffline(i.serverId)}>
                                确认下线
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      </TableCell>
                    </TableRow>
                  ))
                ) : (
                  <TableRow>
                    <TableCell colSpan={12} className="text-center text-muted-foreground">
                      无在册实例
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* 只读实例详情 Dialog：展示列表未呈现的完整信息（metadata 等），不发新请求 */}
      <Dialog open={selectedInstance !== null} onOpenChange={(open) => !open && setSelectedInstance(null)}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>实例详情</DialogTitle>
          </DialogHeader>
          {selectedInstance && (
            <div className="space-y-4">
              <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
                <dt className="text-muted-foreground">serverId</dt>
                <dd className="font-mono">{selectedInstance.serverId}</dd>
                <dt className="text-muted-foreground">环境</dt>
                <dd>{selectedInstance.namespace}</dd>
                <dt className="text-muted-foreground">角色</dt>
                <dd>{selectedInstance.role}</dd>
                <dt className="text-muted-foreground">大区</dt>
                <dd>{selectedInstance.group}</dd>
                <dt className="text-muted-foreground">小区</dt>
                <dd>{selectedInstance.zone === null ? '未分配' : selectedInstance.zone}</dd>
                <dt className="text-muted-foreground">状态</dt>
                <dd>
                  <StatusBadge status={selectedInstance.status} />
                </dd>
                <dt className="text-muted-foreground">地址</dt>
                <dd className="font-mono">{selectedInstance.address}</dd>
                <dt className="text-muted-foreground">版本</dt>
                <dd>{selectedInstance.version}</dd>
                <dt className="text-muted-foreground">容量</dt>
                <dd>{selectedInstance.capacity}</dd>
                <dt className="text-muted-foreground">权重</dt>
                <dd>{selectedInstance.weight}</dd>
                <dt className="text-muted-foreground">人数</dt>
                <dd>{selectedInstance.playerCount}</dd>
                <dt className="text-muted-foreground">TPS</dt>
                <dd>{selectedInstance.tps.toFixed(1)}</dd>
                <dt className="text-muted-foreground">已应用 md5</dt>
                <dd className="font-mono break-all">{selectedInstance.appliedMd5 || '-'}</dd>
                <dt className="text-muted-foreground">最近心跳</dt>
                <dd>{formatTime(selectedInstance.lastHeartbeat)}</dd>
                <dt className="text-muted-foreground">注册时间</dt>
                <dd>{formatTime(selectedInstance.registeredAt)}</dd>
              </dl>
              <div>
                <div className="mb-1.5 text-sm font-medium">metadata</div>
                {Object.keys(selectedInstance.metadata).length > 0 ? (
                  <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 rounded-md bg-muted p-3 text-xs">
                    {Object.entries(selectedInstance.metadata).map(([k, v]) => (
                      <div key={k} className="contents">
                        <dt className="font-mono text-muted-foreground">{k}</dt>
                        <dd className="font-mono break-all">{v}</dd>
                      </div>
                    ))}
                  </dl>
                ) : (
                  <p className="text-sm text-muted-foreground">无 metadata</p>
                )}
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
