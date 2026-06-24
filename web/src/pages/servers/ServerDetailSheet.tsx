// 单服详情 Sheet（FR-65）：点服务器页某行从右侧滑出，按 role 分区展示深指标 + 关系。
// bukkit：人数 / TPS / 容量 / 权重 / 已应用 md5 / 注册时间 / metadata；
// bungee：连接数 / 线程 / 运行时长 / 后端可达性·延迟 + 后端子服清单 + 所属小区默认入口（复用代理服管理页渲染范式 FR-52）。
// 纯只读呈现既有事实，不发新请求（数据来自列表行 InstanceView 与父页已拉的默认入口映射）。

import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import type { InstanceView } from '../../api/types'
import { formatDuration, formatTime } from '../../api/format'
import StatusBadge from '../../components/StatusBadge'
import RoleBadge from '../../components/RoleBadge'
import { Badge } from '@/components/ui/badge'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

// bungee 角色编码（与后端 role 约定一致）
const ROLE_BUNGEE = 'bungee'

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

export default function ServerDetailSheet({ instance, onOpenChange, defaultEntry }: ServerDetailSheetProps) {
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
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
