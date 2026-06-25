// 控制面自身状态条（FR-33）：位于右侧主内容列顶部（侧边栏右侧），「一眼健康」精简版。
// 只呈现连接态药丸 + 版本徽章 + 运行时长 / 在线实例三项核心信号；
// 运行时资源明细（采样器 / goroutine / 堆 / 进程 CPU%）已迁至控制面健康页（FR-82）的「进程运行时」卡。

import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Search } from 'lucide-react'
import { systemStatus } from '@/api/client'
import { formatDuration } from '@/api/format'
import { cn } from '@/lib/utils'
import HeaderControls from '@/components/HeaderControls'
import { useUpdateCheck } from '@/hooks/useUpdateCheck'

// 页眉属性：打开全局命令面板（FR-83，搜索入口由侧栏移至此页眉右上角）。
interface SystemHeaderProps {
  // 点击搜索触发回调：开合态由 Layout 持有，本组件只触发不持有。
  onOpenSearch?: () => void
}

// 自身状态刷新周期（毫秒）：短周期以实时反映控制面健康（含 DB 断开）
const STATUS_REFETCH_MS = 5000

// 单个紧凑指标项：标签 + 值
function StatItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col">
      <span className="text-[11px] leading-none text-muted-foreground">{label}</span>
      <span className="mt-0.5 text-sm font-medium tabular-nums">{children}</span>
    </div>
  )
}

// 加载骨架灰条：首次加载时占位，避免闪 '-'。
function Skeleton({ className }: { className?: string }) {
  return <span className={cn('inline-block h-4 animate-pulse rounded bg-muted', className)} />
}

export default function SystemHeader({ onOpenSearch }: SystemHeaderProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data, isError, isLoading } = useQuery({
    queryKey: ['system-status'],
    queryFn: systemStatus,
    refetchInterval: STATUS_REFETCH_MS,
  })

  // 更新检查（FR-100）：独立低频 query（非 5s 健康轮询），仅驱动版本徽章红点；点击跳「版本与更新」页（ADR-0048，不再弹模态框）。
  const update = useUpdateCheck()

  // 拉取失败（含网络断开）也视作不健康：连接态显红 / 不可达
  const dbConnected = !isError && (data?.db.connected ?? false)

  // 仅 status=ok 且有可用更新、且非 dev 构建时叠红点（check-failed / dev 不提示）
  const hasUpdate = update.data?.status === 'ok' && update.data.hasUpdate && !update.data.isDevBuild
  const version = data?.version ?? '-'

  // 连接态药丸文案：拉取失败=不可达、连通=已连接、未连通=已断开
  const connLabel = isError
    ? t('systemHeader.unreachable')
    : dbConnected
      ? t('systemHeader.connected')
      : t('systemHeader.disconnected')

  // 首次加载（无数据且无错误）显示骨架，不闪 '-'
  const showSkeleton = isLoading && !data && !isError

  return (
    <header className="flex shrink-0 flex-wrap items-center gap-x-6 gap-y-3 border-b bg-background px-6 py-3">
      <div className="flex items-center gap-2">
        <span className="text-sm font-semibold">{t('systemHeader.title')}</span>
        {/* 版本徽章：点击跳「版本与更新」页（ADR-0048）；有可用更新时叠红点（FR-100） */}
        <button
          type="button"
          aria-label={t('systemHeader.versionBadgeAria', { version })}
          onClick={() => navigate('/system/version')}
          className="relative rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground transition-colors hover:bg-muted/70 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          {version}
          {hasUpdate && (
            <span
              role="status"
              aria-label={t('systemHeader.updateAvailableDot')}
              className="absolute -top-1 -right-1 inline-block size-2 rounded-full bg-red-600"
            />
          )}
        </button>
      </div>

      {/* 连接态药丸：绿底绿字=已连接、红底红字=已断开 / 不可达；语义色类、不硬编码 */}
      {showSkeleton ? (
        <Skeleton className="w-20 rounded-full" />
      ) : (
        <span
          className={cn(
            'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium',
            dbConnected
              ? 'bg-green-600/10 text-green-700 dark:text-green-400'
              : 'bg-red-600/10 text-red-700 dark:text-red-400',
          )}
        >
          <span
            aria-hidden
            className={cn('inline-block size-1.5 rounded-full', dbConnected ? 'bg-green-600' : 'bg-red-600')}
          />
          {connLabel}
        </span>
      )}

      {/* 运行 / 在线紧凑一行：运行时长 · 在线实例数 */}
      <StatItem label={t('systemHeader.runtime')}>
        {showSkeleton ? (
          <Skeleton className="w-28" />
        ) : (
          t('systemHeader.runtimeValue', {
            uptime: formatDuration(data?.uptimeSeconds),
            online: data?.onlineInstances ?? '-',
          })
        )}
      </StatItem>

      {/* 右上角操作组（右对齐）：搜索入口 + 界面偏好控件 */}
      <div className="ml-auto flex items-center gap-1">
        {/* 全局搜索入口（FR-83）：由侧栏移至此，点开命令面板浮层，与 Ctrl/Cmd+K 同一浮层 */}
        <button
          type="button"
          onClick={onOpenSearch}
          aria-label={t('commandPalette.trigger')}
          className="flex items-center gap-2 rounded-md px-2 py-1 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
        >
          <Search aria-hidden className="size-4 shrink-0" />
          <span>{t('commandPalette.trigger')}</span>
          <kbd className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
            {t('commandPalette.shortcutHint')}
          </kbd>
        </button>
        {/* 页眉界面偏好控件（FR-92）：主题切换 + 大屏入口 */}
        <HeaderControls />
      </div>
    </header>
  )
}
