// 子系统健康面板：连接池 / 长轮询 / 注册表 / 命令队列的仪表环总览 + 逐项明细列表。
import { useTranslation } from 'react-i18next'
import { Database, Radio, ServerCog, SquareTerminal } from 'lucide-react'

import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  GaugeRing,
  ratioLevel,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@beacon/ui'
import type { SystemObservability } from '@beacon/devmock'

import { formatCount } from '../../features/system/format'

export default function SubsystemPanel({ observability }: { observability: SystemObservability }) {
  const { t } = useTranslation()
  const { dbPool, longpoll, registryByStatus, registryTotal, commandByStatus } = observability

  const poolRatio = dbPool.maxOpenConnections === 0 ? 0 : dbPool.inUse / dbPool.maxOpenConnections
  // Record 索引在本工程默认返回 number（未开 noUncheckedIndexedAccess），缺键回退 0
  const commandFailed = commandByStatus.failed as number | undefined ?? 0

  return (
    <div className="grid gap-4">
      {/* 子系统仪表环总览 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t('system.health.subsystems.title')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-wrap justify-around gap-4">
          <GaugeRing
            icon={<Database className="size-5" />}
            ratio={poolRatio}
            level={ratioLevel(poolRatio)}
            label={t('system.health.subsystems.dbPool')}
            valueText={t('system.health.subsystems.dbPoolValue', {
              inUse: dbPool.inUse,
              max: dbPool.maxOpenConnections,
            })}
          />
          <GaugeRing
            icon={<Radio className="size-5" />}
            ratio={null}
            level="ok"
            label={t('system.health.subsystems.longpoll')}
            valueText={formatCount(longpoll.total)}
          />
          <GaugeRing
            icon={<ServerCog className="size-5" />}
            ratio={null}
            level="ok"
            label={t('system.health.subsystems.registry')}
            valueText={formatCount(registryTotal)}
          />
          <GaugeRing
            icon={<SquareTerminal className="size-5" />}
            ratio={null}
            level={commandFailed > 0 ? 'warn' : 'ok'}
            label={t('system.health.subsystems.command')}
            valueText={formatCount(commandFailed)}
          />
        </CardContent>
      </Card>

      {/* 连接池明细 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t('system.health.detail.dbPoolTitle')}</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
          <DetailRow label={t('system.health.detail.maxOpen')} value={formatCount(dbPool.maxOpenConnections)} />
          <DetailRow label={t('system.health.detail.open')} value={formatCount(dbPool.openConnections)} />
          <DetailRow label={t('system.health.detail.inUse')} value={formatCount(dbPool.inUse)} />
          <DetailRow label={t('system.health.detail.idle')} value={formatCount(dbPool.idle)} />
          <DetailRow label={t('system.health.detail.waitCount')} value={formatCount(dbPool.waitCount)} />
          <DetailRow label={t('system.health.detail.waitDuration')} value={formatCount(dbPool.waitDurationMs)} />
        </CardContent>
      </Card>

      {/* 长轮询挂起明细 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t('system.health.detail.longpollTitle')}</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-5">
          <DetailRow label={t('system.health.detail.config')} value={formatCount(longpoll.config)} />
          <DetailRow label={t('system.health.detail.file')} value={formatCount(longpoll.file)} />
          <DetailRow label={t('system.health.detail.topology')} value={formatCount(longpoll.topology)} />
          <DetailRow label={t('system.health.detail.commandLp')} value={formatCount(longpoll.command)} />
          <DetailRow label={t('system.health.detail.total')} value={formatCount(longpoll.total)} />
        </CardContent>
      </Card>

      {/* 注册表 / 命令队列状态计数 */}
      <div className="grid gap-4 sm:grid-cols-2">
        <StatusCountCard title={t('system.health.detail.registryTitle')} counts={registryByStatus} />
        <StatusCountCard title={t('system.health.detail.commandTitle')} counts={commandByStatus} />
      </div>
    </div>
  )
}

function DetailRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium tabular-nums">{value}</span>
    </div>
  )
}

function StatusCountCard({ title, counts }: { title: string; counts: Record<string, number> }) {
  const { t } = useTranslation()
  const entries = Object.entries(counts)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{title}</CardTitle>
      </CardHeader>
      <CardContent>
        {entries.length === 0 ? (
          <p className="text-sm text-muted-foreground">-</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('system.health.detail.status')}</TableHead>
                <TableHead className="text-right">{t('system.health.detail.count')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.map(([status, count]) => (
                <TableRow key={status}>
                  <TableCell className="font-mono text-xs">{status}</TableCell>
                  <TableCell className="text-right tabular-nums">{formatCount(count)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}
