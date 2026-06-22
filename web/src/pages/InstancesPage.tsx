// 实例与健康页：按 namespace/group/zone/role/status 过滤，5 秒轮询健康。
// online/lost/offline 三色区分；未分配 zone 的行高亮；点行看只读详情；支持主动下线（按行直接下线，不再强制先筛环境，FR-49）。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  listInstances,
  listNamespaces,
  listOfflineInstances,
  offlineInstance,
  onlineInstance,
  zoneSummary,
} from '../api/client'
import type { InstanceFilter } from '../api/client'
import type { InstanceView } from '../api/types'
import { formatTime } from '../api/format'
import StatusBadge from '../components/StatusBadge'
import RoleBadge from '../components/RoleBadge'
import { useMessage } from '../components/useMessage'
import AsyncSection from '@/components/AsyncSection'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { Combobox } from '@/components/ui/combobox'
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
  const { t } = useTranslation()
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

  // 主动下线标记（FR-49）：已下线实例不在注册表列表出现，单列展示并提供「取消下线」。
  const { data: offlineMarkers } = useQuery({
    queryKey: ['offline-instances', filter.namespace],
    queryFn: () => listOfflineInstances(filter.namespace),
    refetchInterval: REFETCH_MS,
  })

  // 筛选维度下拉的候选来源（FR-51）：环境来自 listNamespaces，大区 / 小区由 zone 汇总与全量实例派生。
  // 候选不随当前过滤收窄（全量拉取），且筛选框允许键入候选外的值（可编辑）。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const allInstancesQuery = useQuery({
    queryKey: ['instances', 'filter-options'],
    queryFn: () => listInstances({}),
  })
  const zoneSummaryQuery = useQuery({ queryKey: ['zone-summary', 'all'], queryFn: () => zoneSummary() })

  const namespaceOptions = useMemo(
    () => (namespacesQuery.data ?? []).map((n) => n.code),
    [namespacesQuery.data],
  )
  // 大区候选：zone 汇总与实例列表去重并集（兼容无 zone 指派但已注册的大区）
  const groupOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of zoneSummaryQuery.data ?? []) if (z.group) set.add(z.group)
    for (const i of allInstancesQuery.data ?? []) if (i.group) set.add(i.group)
    return Array.from(set).sort()
  }, [zoneSummaryQuery.data, allInstancesQuery.data])
  // 小区候选：zone 汇总与实例列表去重并集
  const zoneOptions = useMemo(() => {
    const set = new Set<string>()
    for (const z of zoneSummaryQuery.data ?? []) if (z.zone) set.add(z.zone)
    for (const i of allInstancesQuery.data ?? []) if (i.zone) set.add(i.zone)
    return Array.from(set).sort()
  }, [zoneSummaryQuery.data, allInstancesQuery.data])

  // 主动下线：namespace 取自该行实例，不再强制先在过滤条件中选环境（FR-49）。
  const offlineMut = useMutation({
    mutationFn: ({ serverId, namespace: ns }: { serverId: string; namespace: string }) =>
      offlineInstance(serverId, ns),
    onSuccess: (_data, { serverId }) => {
      msg.showSuccess(t('instances.msgOffline', { serverId }))
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['offline-instances'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 取消主动下线：清除拒绝态，使实例可重新接入（FR-49）。
  const onlineMut = useMutation({
    mutationFn: ({ serverId, namespace: ns }: { serverId: string; namespace: string }) =>
      onlineInstance(serverId, ns),
    onSuccess: (_data, { serverId }) => {
      msg.showSuccess(t('instances.msgCancelOffline', { serverId }))
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['offline-instances'] })
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

  // 实例表列定义（操作列闭包引用 offlineMut / onConfirmOffline，故在组件内定义）
  const columns: DataTableColumn<InstanceView>[] = [
    { header: 'serverId', className: 'font-mono', cell: (i) => i.serverId },
    { header: t('instances.colNamespace'), cell: (i) => i.namespace },
    { header: t('instances.colRole'), cell: (i) => <RoleBadge role={i.role} /> },
    { header: t('instances.colGroup'), cell: (i) => i.group },
    {
      header: t('instances.colZone'),
      cell: (i) =>
        i.zone === null ? (
          <Badge variant="outline" className="border-amber-500 text-amber-600">
            {t('instances.unassignedBadge')}
          </Badge>
        ) : (
          i.zone
        ),
    },
    { header: t('instances.colStatus'), cell: (i) => <StatusBadge status={i.status} /> },
    { header: t('instances.colAddress'), className: 'font-mono', cell: (i) => i.address },
    { header: t('instances.colVersion'), cell: (i) => i.version },
    { header: t('instances.colPlayerCount'), cell: (i) => i.playerCount },
    { header: t('instances.colTps'), cell: (i) => i.tps.toFixed(1) },
    { header: t('instances.colLastHeartbeat'), cell: (i) => formatTime(i.lastHeartbeat) },
    {
      header: t('instances.colActions'),
      cell: (i) => (
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button
              variant="destructive"
              size="sm"
              disabled={offlineMut.isPending}
              onClick={(e) => e.stopPropagation()}
            >
              {t('instances.offlineBtn')}
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent onClick={(e) => e.stopPropagation()}>
            <AlertDialogHeader>
              <AlertDialogTitle>{t('instances.offlineConfirmTitle', { serverId: i.serverId })}</AlertDialogTitle>
              <AlertDialogDescription>
                {t('instances.offlineConfirmDesc', { namespace: i.namespace })}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
              <AlertDialogAction
                onClick={() => offlineMut.mutate({ serverId: i.serverId, namespace: i.namespace })}
              >
                {t('instances.offlineConfirmAction')}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      ),
    },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{t('instances.title')}</h1>
        {isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
      </div>

      <Card>
        <CardContent className="space-y-3">
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="f-namespace">{t('common.namespace')}</Label>
              {/* 筛选框：可编辑下拉，候选来自 API 但允许键入列表外值（FR-51） */}
              <Combobox
                id="f-namespace"
                aria-label={t('common.namespace')}
                className="w-40"
                value={namespace}
                onChange={setNamespace}
                options={namespaceOptions}
                allowCustom
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-group">{t('common.group')}</Label>
              <Combobox
                id="f-group"
                aria-label={t('common.group')}
                className="w-40"
                value={group}
                onChange={setGroup}
                options={groupOptions}
                allowCustom
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-zone">{t('common.zone')}</Label>
              <Combobox
                id="f-zone"
                aria-label={t('common.zone')}
                className="w-40"
                value={zone}
                onChange={setZone}
                options={zoneOptions}
                allowCustom
              />
            </div>
            <div className="space-y-1.5">
              <Label>{t('common.role')}</Label>
              <Select value={role} onValueChange={setRole}>
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('instances.filterAll')}</SelectItem>
                  <SelectItem value="bukkit">bukkit</SelectItem>
                  <SelectItem value="bungee">bungee</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>{t('common.status')}</Label>
              <Select value={status} onValueChange={setStatus}>
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('instances.filterAll')}</SelectItem>
                  <SelectItem value="online">online</SelectItem>
                  <SelectItem value="lost">lost</SelectItem>
                  <SelectItem value="offline">offline</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button type="submit">{t('common.query')}</Button>
          </form>
          <p className="text-sm text-muted-foreground">
            {t('instances.tip')}
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardContent>
          <AsyncSection isLoading={isLoading} isError={isError} error={error}>
            <DataTable
              columns={columns}
              rows={data}
              rowKey={(i) => `${i.namespace}/${i.serverId}`}
              emptyText={t('instances.empty')}
              onRowClick={(i) => setSelectedInstance(i)}
              rowClassName={(i) => (!i.assigned ? 'bg-amber-50' : undefined)}
            />
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 已主动下线标记（FR-49）：已下线实例不在上表（已移出可用集），单列展示并支持取消下线 */}
      {offlineMarkers && offlineMarkers.length > 0 && (
        <Card>
          <CardContent className="space-y-3">
            <h2 className="text-base font-semibold">{t('instances.offlineSectionTitle')}</h2>
            <DataTable
              columns={[
                { header: 'serverId', className: 'font-mono', cell: (o) => o.serverId },
                { header: t('instances.colNamespace'), cell: (o) => o.namespace },
                { header: t('instances.offlineColReason'), cell: (o) => o.reason || '-' },
                {
                  header: t('instances.offlineColActions'),
                  cell: (o) => (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={onlineMut.isPending}
                      onClick={() => onlineMut.mutate({ serverId: o.serverId, namespace: o.namespace })}
                    >
                      {t('instances.cancelOfflineBtn')}
                    </Button>
                  ),
                },
              ]}
              rows={offlineMarkers}
              rowKey={(o) => `${o.namespace}/${o.serverId}`}
              emptyText={t('instances.offlineEmpty')}
            />
          </CardContent>
        </Card>
      )}

      {/* 只读实例详情 Dialog：展示列表未呈现的完整信息（metadata 等），不发新请求 */}
      <Dialog open={selectedInstance !== null} onOpenChange={(open) => !open && setSelectedInstance(null)}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('instances.detailTitle')}</DialogTitle>
          </DialogHeader>
          {selectedInstance && (
            <div className="space-y-4">
              <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
                <dt className="text-muted-foreground">serverId</dt>
                <dd className="font-mono">{selectedInstance.serverId}</dd>
                <dt className="text-muted-foreground">{t('instances.colNamespace')}</dt>
                <dd>{selectedInstance.namespace}</dd>
                <dt className="text-muted-foreground">{t('instances.colRole')}</dt>
                <dd>
                  <RoleBadge role={selectedInstance.role} />
                </dd>
                <dt className="text-muted-foreground">{t('instances.colGroup')}</dt>
                <dd>{selectedInstance.group}</dd>
                <dt className="text-muted-foreground">{t('instances.colZone')}</dt>
                <dd>{selectedInstance.zone === null ? t('common.unassigned') : selectedInstance.zone}</dd>
                <dt className="text-muted-foreground">{t('instances.colStatus')}</dt>
                <dd>
                  <StatusBadge status={selectedInstance.status} />
                </dd>
                <dt className="text-muted-foreground">{t('instances.colAddress')}</dt>
                <dd className="font-mono">{selectedInstance.address}</dd>
                <dt className="text-muted-foreground">{t('instances.colVersion')}</dt>
                <dd>{selectedInstance.version}</dd>
                <dt className="text-muted-foreground">{t('instances.detailCapacity')}</dt>
                <dd>{selectedInstance.capacity}</dd>
                <dt className="text-muted-foreground">{t('instances.detailWeight')}</dt>
                <dd>{selectedInstance.weight}</dd>
                <dt className="text-muted-foreground">{t('instances.colPlayerCount')}</dt>
                <dd>{selectedInstance.playerCount}</dd>
                <dt className="text-muted-foreground">{t('instances.colTps')}</dt>
                <dd>{selectedInstance.tps.toFixed(1)}</dd>
                <dt className="text-muted-foreground">{t('instances.detailAppliedMd5')}</dt>
                <dd className="font-mono break-all">{selectedInstance.appliedMd5 || '-'}</dd>
                <dt className="text-muted-foreground">{t('instances.colLastHeartbeat')}</dt>
                <dd>{formatTime(selectedInstance.lastHeartbeat)}</dd>
                <dt className="text-muted-foreground">{t('instances.detailRegisteredAt')}</dt>
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
                  <p className="text-sm text-muted-foreground">{t('instances.noMetadata')}</p>
                )}
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
