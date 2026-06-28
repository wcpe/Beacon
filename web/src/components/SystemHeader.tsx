// 控制面自身状态条（FR-33 / FR-105 真机打磨）：作为「贯穿整宽顶栏」的右侧区内容，占满品牌区之外的剩余宽度。
// 只呈现连接态药丸 + 版本徽章 + 运行时长 / 在线实例三项核心信号；
// 运行时资源明细（采样器 / goroutine / 堆 / 进程 CPU%）已迁至控制面健康页（FR-82）的「进程运行时」卡。
// 注：本组件只渲染「状态条内容」，不再持有自己的 header 外壳（border-b / px / py 由顶栏容器统一），高度由顶栏压低（~40px）。

import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Search } from 'lucide-react'
import { systemStatus } from '@/api/client'
import { formatDuration } from '@/api/format'
import { cn } from '@/lib/utils'
import HeaderControls from '@/components/HeaderControls'
import OperatorMenu from '@/components/OperatorMenu'

// 页眉属性：打开全局命令面板（FR-83，搜索入口由侧栏移至此页眉右上角）。
interface SystemHeaderProps {
  // 点击搜索触发回调：开合态由 Layout 持有，本组件只触发不持有。
  onOpenSearch?: () => void
}

// 自身状态刷新周期（毫秒）：短周期以实时反映控制面健康（含 DB 断开）
const STATUS_REFETCH_MS = 5000

// 加载骨架灰条：首次加载时占位，避免闪 '-'。
function Skeleton({ className }: { className?: string }) {
  return <span className={cn('inline-block h-4 animate-pulse rounded bg-muted', className)} />
}

export default function SystemHeader({ onOpenSearch }: SystemHeaderProps) {
  const { t } = useTranslation()
  const { data, isError, isLoading } = useQuery({
    queryKey: ['system-status'],
    queryFn: systemStatus,
    refetchInterval: STATUS_REFETCH_MS,
  })

  // 拉取失败（含网络断开）也视作不健康：连接态显红 / 不可达
  const dbConnected = !isError && (data?.db.connected ?? false)

  // 连接态药丸文案：拉取失败=不可达、连通=已连接、未连通=已断开
  const connLabel = isError
    ? t('systemHeader.unreachable')
    : dbConnected
      ? t('systemHeader.connected')
      : t('systemHeader.disconnected')

  // 首次加载（无数据且无错误）显示骨架，不闪 '-'
  const showSkeleton = isLoading && !data && !isError

  return (
    // 只渲染状态条内容（不含 header 外壳）：由顶栏容器统一边框/内边距，高度压低后用更紧凑的纵向留白。
    <div className="flex w-full flex-wrap items-center gap-x-6 gap-y-1.5">
      {/* 控制面状态条标题；版本徽章已移至整宽顶栏品牌区 logo 右侧（FR-121，见 VersionBadge） */}
      <span className="text-sm font-semibold">{t('systemHeader.title')}</span>

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

      {/* 运行 / 在线紧凑一行（FR-118 E：去标签行，仅留「运行 X · 在线 N」一行） */}
      {showSkeleton ? (
        <Skeleton className="w-28" />
      ) : (
        <span className="text-sm font-medium tabular-nums">
          {t('systemHeader.runtimeValue', {
            uptime: formatDuration(data?.uptimeSeconds),
            online: data?.onlineInstances ?? '-',
          })}
        </span>
      )}

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
        {/* 账户菜单（FR-121）：首字母头像 + 下拉（操作人全名 + 登出），从侧栏底部移来 */}
        <OperatorMenu />
      </div>
    </div>
  )
}
