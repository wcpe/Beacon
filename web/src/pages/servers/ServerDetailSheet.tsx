// 单服详情 Sheet（FR-65）：点服务器页某行从右侧滑出，按 role 分区展示深指标 + 关系。
// bukkit：人数 / TPS / 容量 / 权重 / 已应用 md5 / 注册时间 / metadata；
// bungee：连接数 / 线程 / 运行时长 / 后端可达性·延迟 + 后端子服清单 + 所属小区默认入口（复用代理服管理页渲染范式 FR-52）。
// 指标区纯只读呈现列表行 InstanceView 既有事实；变更历史区（FR-80）打开时按 serverId 拉有效配置变更时间线。

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { useMutation, useQuery } from '@tanstack/react-query'
import type { AgentLogLine, ConfigTimelineEntry, InstanceView } from '../../api/types'
import { getAgentLogs, requestAgentLogs, serverConfigTimeline } from '../../api/client'
import { formatDuration, formatTime } from '../../api/format'
import StatusBadge from '../../components/StatusBadge'
import RoleBadge from '../../components/RoleBadge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

// bungee 角色编码（与后端 role 约定一致）
const ROLE_BUNGEE = 'bungee'

// 覆盖层 scopeLevel → i18n key（集中映射，消灭散落 if）
const SCOPE_LABEL_KEY: Record<string, string> = {
  global: 'servers.timelineScopeGlobal',
  group: 'servers.timelineScopeGroup',
  zone: 'servers.timelineScopeZone',
  server: 'servers.timelineScopeServer',
}

// scopeLabel 渲染某条目的覆盖层文案（未知层回退原始 scopeLevel）
function scopeLabel(t: TFunction, scopeLevel: string): string {
  const key = SCOPE_LABEL_KEY[scopeLevel]
  return key ? t(key) : scopeLevel
}

// 后端可达性文案：有配置后端时显示 up/total，无后端显示「无后端」
function reachText(t: TFunction, up: number, total: number): string {
  return total > 0 ? `${up} / ${total}` : t('servers.noBackend')
}

// 后端平均延迟文案：< 0（约定 -1）表示无可达后端样本，显示「不可用」而非负数
function latencyText(t: TFunction, ms: number): string {
  return ms >= 0 ? `${ms.toFixed(0)} ms` : t('servers.unavailable')
}

interface ServerDetailSheetProps {
  // 选中的实例（null 表示关闭 Sheet）
  instance: InstanceView | null
  onOpenChange: (open: boolean) => void
  // 该实例所属小区默认入口 serverId（仅 bungee 用，FR-48；未设/未分配 zone 时 undefined）
  defaultEntry?: string
  // 该实例 agent 版本是否与本环境多数不一致（FR-86）：由父页按多数版本算好传入，详情区打黄标
  agentVersionMismatch?: boolean
}

// 单行键值对（定义列表行）
function Field({ label, children, mono }: { label: string; children: React.ReactNode; mono?: boolean }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? 'font-mono break-all' : undefined}>{children}</dd>
    </>
  )
}

// TimelineSection 变更历史区（FR-80）：列该服覆盖链涉及 config 项的发布记录（按时间倒序）。
// Sheet 打开（instance 非空）时才拉，关闭即随 queryKey 失活；三态：加载 / 失败 / 空 / 列表。
function TimelineSection({ instance }: { instance: InstanceView }) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['server-config-timeline', instance.namespace, instance.serverId, instance.group],
    queryFn: () =>
      serverConfigTimeline({
        serverId: instance.serverId,
        namespace: instance.namespace,
        group: instance.group,
      }),
  })
  return (
    <section className="space-y-3">
      <div>
        <h3 className="text-sm font-medium">{t('servers.timelineSection')}</h3>
        <p className="text-xs text-muted-foreground">{t('servers.timelineHint')}</p>
      </div>
      {query.isLoading ? (
        <p className="text-sm text-muted-foreground">{t('servers.timelineLoading')}</p>
      ) : query.isError ? (
        <p className="text-sm text-destructive">{t('servers.timelineError')}</p>
      ) : query.data && query.data.items.length > 0 ? (
        <ol className="space-y-2">
          {query.data.items.map((e: ConfigTimelineEntry) => (
            <li
              key={`${e.configItemId}-${e.version}`}
              className="rounded-md border border-border p-2.5 text-sm"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono break-all">{e.dataId}</span>
                <Badge variant="outline">{scopeLabel(t, e.scopeLevel)}</Badge>
                <Badge variant="secondary">{t('servers.timelineVersion', { version: e.version })}</Badge>
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                <span>{formatTime(e.createdAt)}</span>
                <span>{e.operator}</span>
              </div>
              {e.comment && <p className="mt-1 text-xs break-all">{e.comment}</p>}
            </li>
          ))}
        </ol>
      ) : (
        <p className="text-sm text-muted-foreground">{t('servers.timelineEmpty')}</p>
      )}
    </section>
  )
}

// 单行日志级别 → 着色（INFO 默认 / WARN 琥珀 / ERROR 红）
function logLineClass(level: string): string {
  if (level === 'ERROR') return 'text-destructive'
  if (level === 'WARN') return 'text-amber-600'
  return 'text-muted-foreground'
}

// LogsSection agent 日志区（FR-88，见 ADR-0040）：点按钮触发取日志（命令-回传）→ 轮询命令状态至 done/failed/expired
// → 展示该 agent 自身最近日志（脱敏后）。仅 agent 自身日志、不读任意文件；轮询仅在有进行中命令时启用。
function LogsSection({ instance }: { instance: InstanceView }) {
  const { t } = useTranslation()
  // 已触发的命令 id（点过按钮后才开始轮询；null 表示尚未触发本次）
  const [commandId, setCommandId] = useState<number | null>(null)

  // 轮询查询：拉最近一次取日志结果；进行中（pending/fetched）时每秒重拉，到终态停止。
  const query = useQuery({
    queryKey: ['agent-logs', instance.namespace, instance.serverId, commandId],
    queryFn: () => getAgentLogs(instance.serverId, instance.namespace),
    enabled: commandId !== null,
    refetchInterval: (q) => {
      const s = q.state.data?.status
      return s === 'pending' || s === 'fetched' ? 1000 : false
    },
  })

  const triggerMut = useMutation({
    mutationFn: () => requestAgentLogs(instance.serverId, instance.namespace),
    onSuccess: (cmd) => setCommandId(cmd.commandId),
  })

  const status = query.data?.status
  const inProgress = triggerMut.isPending || status === 'pending' || status === 'fetched'
  const lines: AgentLogLine[] = query.data?.lines ?? []

  return (
    <section className="space-y-3">
      <div>
        <h3 className="text-sm font-medium">{t('servers.logsSection')}</h3>
        <p className="text-xs text-muted-foreground">{t('servers.logsHint')}</p>
      </div>
      <Button size="sm" variant="outline" disabled={inProgress} onClick={() => triggerMut.mutate()}>
        {commandId === null ? t('servers.logsFetch') : t('servers.logsRefresh')}
      </Button>
      {triggerMut.isError ? (
        <p className="text-sm text-destructive">{(triggerMut.error as Error).message}</p>
      ) : inProgress ? (
        <p className="text-sm text-muted-foreground">{t('servers.logsPending')}</p>
      ) : query.isError ? (
        <p className="text-sm text-destructive">{t('servers.logsError')}</p>
      ) : status === 'failed' || status === 'expired' ? (
        <p className="text-sm text-destructive">{t('servers.logsFailedStatus')}</p>
      ) : status === 'done' ? (
        lines.length > 0 ? (
          <pre className="max-h-80 overflow-auto rounded-md bg-muted p-3 text-xs leading-relaxed">
            {lines.map((l, i) => (
              <div key={i} className={`font-mono break-all ${logLineClass(l.level)}`}>
                <span className="select-none opacity-60">[{l.level}]</span> {l.text}
              </div>
            ))}
          </pre>
        ) : (
          <p className="text-sm text-muted-foreground">{t('servers.logsEmpty')}</p>
        )
      ) : null}
    </section>
  )
}

export default function ServerDetailSheet({
  instance,
  onOpenChange,
  defaultEntry,
  agentVersionMismatch = false,
}: ServerDetailSheetProps) {
  const { t } = useTranslation()
  const isBungee = instance?.role === ROLE_BUNGEE
  return (
    <Sheet open={instance !== null} onOpenChange={onOpenChange}>
      <SheetContent className="w-full overflow-y-auto p-6 sm:max-w-lg">
        {instance && (
          <>
            <SheetHeader className="p-0">
              <SheetTitle className="flex flex-wrap items-center gap-2">
                <span className="font-mono">{instance.serverId}</span>
                <RoleBadge role={instance.role} />
                <StatusBadge status={instance.status} reason={instance.healthReason} />
              </SheetTitle>
            </SheetHeader>

            {/* 公共标识：环境 / 大区 / 小区 / 地址 / 版本 */}
            <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
              <Field label={t('servers.colNamespace')}>{instance.namespace}</Field>
              <Field label={t('servers.colGroup')}>{instance.group}</Field>
              <Field label={t('servers.colZone')}>
                {instance.zone === null ? t('common.unassigned') : instance.zone}
              </Field>
              <Field label={t('servers.colAddress')} mono>
                {instance.address}
              </Field>
              <Field label={t('servers.colVersion')}>{instance.version}</Field>
              <Field label={t('servers.colAgentVersion')}>
                {instance.agentVersion ? (
                  agentVersionMismatch ? (
                    <Badge
                      variant="outline"
                      className="border-amber-500 font-mono text-amber-600"
                      title={t('servers.agentVersionMismatch')}
                    >
                      {instance.agentVersion}
                    </Badge>
                  ) : (
                    <span className="font-mono">{instance.agentVersion}</span>
                  )
                ) : (
                  <span className="text-muted-foreground">{t('servers.agentVersionUnknown')}</span>
                )}
              </Field>
              <Field label={t('servers.colLastHeartbeat')}>{formatTime(instance.lastHeartbeat)}</Field>
              <Field label={t('servers.detailRegisteredAt')}>{formatTime(instance.registeredAt)}</Field>
            </dl>

            {isBungee ? (
              /* ===== bungee 深指标（FR-34/36/48）===== */
              <section className="space-y-3">
                <h3 className="text-sm font-medium">{t('servers.detailProxySection')}</h3>
                <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
                  <Field label={t('servers.cardConnections')}>{instance.proxy.onlineConnections}</Field>
                  <Field label={t('servers.cardThreads')}>{instance.proxy.threadCount}</Field>
                  <Field label={t('servers.cardUptime')}>{formatDuration(instance.proxy.uptimeMs / 1000)}</Field>
                  <Field label={t('servers.cardBackendReach')}>
                    {reachText(t, instance.proxy.backendUp, instance.proxy.backendTotal)}
                  </Field>
                  <Field label={t('servers.cardBackendLatency')}>
                    {latencyText(t, instance.proxy.backendAvgLatencyMs)}
                  </Field>
                </dl>
                {/* 后端子服清单（FR-36） */}
                <div>
                  <div className="mb-1.5 text-sm font-medium">
                    {t('servers.backendsTitle', { count: instance.backends.length })}
                  </div>
                  {instance.backends.length > 0 ? (
                    <div className="flex flex-wrap gap-1.5">
                      {instance.backends.map((b) => (
                        <Badge key={b} variant="secondary" className="font-mono">
                          {b}
                        </Badge>
                      ))}
                    </div>
                  ) : (
                    <p className="text-sm text-muted-foreground">{t('servers.noBackend')}</p>
                  )}
                </div>
                {/* 所属小区默认入口（FR-48） */}
                <div className="flex items-center gap-2 text-sm">
                  <span className="text-muted-foreground">{t('servers.defaultEntryLabel')}</span>
                  {defaultEntry ? (
                    <Badge variant="outline" className="font-mono">
                      {defaultEntry}
                    </Badge>
                  ) : (
                    <span className="text-muted-foreground">{t('servers.defaultEntryUnset')}</span>
                  )}
                </div>
              </section>
            ) : (
              /* ===== bukkit 深指标 ===== */
              <section className="space-y-3">
                <h3 className="text-sm font-medium">{t('servers.detailBukkitSection')}</h3>
                <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
                  <Field label={t('servers.colPlayerCount')}>{instance.playerCount}</Field>
                  <Field label={t('servers.colTps')}>{instance.tps.toFixed(1)}</Field>
                  <Field label={t('servers.detailCapacity')}>{instance.capacity}</Field>
                  <Field label={t('servers.detailWeight')}>{instance.weight}</Field>
                  <Field label={t('servers.detailDefaultEntry')}>
                    {instance.zoneDefaultEntry ? t('common.confirm') : '-'}
                  </Field>
                  <Field label={t('servers.detailAppliedMd5')} mono>
                    {instance.appliedMd5 || '-'}
                  </Field>
                </dl>
              </section>
            )}

            {/* metadata（公共，仅 bukkit 常用但 bungee 也可能带） */}
            <div>
              <div className="mb-1.5 text-sm font-medium">metadata</div>
              {Object.keys(instance.metadata).length > 0 ? (
                <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1 rounded-md bg-muted p-3 text-xs">
                  {Object.entries(instance.metadata).map(([k, v]) => (
                    <div key={k} className="contents">
                      <dt className="font-mono text-muted-foreground">{k}</dt>
                      <dd className="font-mono break-all">{v}</dd>
                    </div>
                  ))}
                </dl>
              ) : (
                <p className="text-sm text-muted-foreground">{t('servers.noMetadata')}</p>
              )}
            </div>

            {/* 变更历史时间线（FR-80） */}
            <TimelineSection instance={instance} />

            {/* agent 日志（FR-88，见 ADR-0040） */}
            <LogsSection instance={instance} />
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
