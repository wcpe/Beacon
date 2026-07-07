// 进程运行时卡片：版本 / 运行时长 / DB 连通 / 在线实例 / 采样器 / 协程 / 堆 / CPU。
import { useTranslation } from 'react-i18next'
import { Activity, Cpu, Database, MemoryStick, Server, Timer } from 'lucide-react'

import { Badge, Card, CardContent, CardHeader, CardTitle, IconStat, ratioLevel } from '@beacon/ui'
import type { SystemStatus } from '@beacon/devmock'

import { formatBytes, formatCount, formatDuration } from '../../features/system/format'

export default function RuntimeCard({ status }: { status: SystemStatus }) {
  const { t } = useTranslation()

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t('system.health.runtime.title')}</CardTitle>
      </CardHeader>
      <CardContent className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <IconStat icon={<Timer className="size-4" />} label={t('system.health.runtime.version')} value={status.version} />
        <IconStat
          icon={<Timer className="size-4" />}
          label={t('system.health.runtime.uptime')}
          value={formatDuration(status.uptimeSeconds)}
        />
        <IconStat
          icon={<Database className="size-4" />}
          label={t('system.health.runtime.db')}
          value={
            <Badge variant={status.db.connected ? 'secondary' : 'destructive'}>
              {status.db.connected
                ? t('system.health.runtime.dbConnected')
                : t('system.health.runtime.dbDisconnected')}
            </Badge>
          }
        />
        <IconStat
          icon={<Server className="size-4" />}
          label={t('system.health.runtime.online')}
          value={formatCount(status.onlineInstances)}
        />
        <IconStat
          icon={<Activity className="size-4" />}
          label={t('system.health.runtime.sampler')}
          value={
            <Badge variant={status.samplerEnabled ? 'secondary' : 'outline'}>
              {status.samplerEnabled ? t('system.health.runtime.samplerOn') : t('system.health.runtime.samplerOff')}
            </Badge>
          }
        />
        <IconStat
          icon={<Activity className="size-4" />}
          label={t('system.health.runtime.goroutines')}
          value={formatCount(status.runtime.goroutines)}
        />
        <IconStat
          icon={<MemoryStick className="size-4" />}
          label={t('system.health.runtime.heap')}
          value={`${formatBytes(status.runtime.heapAlloc)} / ${formatBytes(status.runtime.heapSys)}`}
        />
        <IconStat
          icon={<Cpu className="size-4" />}
          label={t('system.health.runtime.cpu')}
          value={status.cpuAvailable ? `${status.cpuPercent.toFixed(1)}%` : t('system.health.runtime.cpuUnavailable')}
          level={status.cpuAvailable ? ratioLevel(status.cpuPercent / 100) : 'muted'}
        />
      </CardContent>
    </Card>
  )
}
