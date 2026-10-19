// 页眉刷新当前页（FR-196）：按路由前缀 invalidate react-query，不整页 reload。
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router-dom'
import { RefreshCw } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { Button } from '@beacon/ui'

import { beginPageRefreshing, endPageRefreshing } from './page-refreshing'

/**
 * 当前路径 → 应失效的 queryKey 前缀列表。
 * 匹配页面内 useQuery 的主 key，尽量保留 URL/筛选（筛选在组件 state，invalidate 只重拉数据）。
 */
export function queryKeysForPath(pathname: string): string[][] {
  if (pathname.startsWith('/dashboard')) {
    return [['dashboard'], ['shell', 'metrics']]
  }
  if (pathname.startsWith('/servers') || pathname.startsWith('/identity-conflicts')) {
    return [['servers'], ['identities'], ['health']]
  }
  if (pathname.startsWith('/zones')) {
    return [['zone-tree'], ['servers'], ['identities']]
  }
  if (pathname.startsWith('/topology')) {
    return [['zone-tree'], ['servers'], ['health'], ['messages']]
  }
  if (pathname.startsWith('/service-analysis')) {
    return [['service-analysis'], ['servers']]
  }
  if (pathname.startsWith('/alert-events')) {
    return [['alert-events'], ['dashboard', 'alerts'], ['shell', 'notifications'], ['shell', 'metrics']]
  }
  if (pathname.startsWith('/commands')) {
    return [['commands']]
  }
  if (pathname.startsWith('/audits')) {
    return [['audits']]
  }
  if (pathname.startsWith('/connections') || pathname.startsWith('/messages')) {
    return [['connections'], ['messages']]
  }
  if (pathname.startsWith('/assets')) {
    return [['assets'], ['file-assets']]
  }
  if (pathname.startsWith('/configs')) {
    return [['configs'], ['config-files']]
  }
  if (pathname.startsWith('/changes')) {
    return [['change-orders']]
  }
  if (pathname.startsWith('/settings') || pathname.startsWith('/system')) {
    return [['settings'], ['system'], ['shell']]
  }
  if (pathname.startsWith('/api-keys')) {
    return [['api-keys']]
  }
  if (pathname.startsWith('/namespaces')) {
    return [['namespaces']]
  }
  if (pathname.startsWith('/envs')) {
    return [['envs']]
  }
  // 兜底：失效全部（仍不 reload）
  return []
}

/**
 * 刷新忙碌态的最短展示时长（ms）。
 * 本机或内网请求常在数十毫秒内返回，若只按请求耗时控制忙碌态，变淡/脉冲会一闪而过，
 * 用户根本看不见，等于没有反馈。给一个下限，让「正在刷新」至少可被感知。
 * 取值与图标旋转的 400ms 对齐，两者的视觉节奏一致。
 */
export const MIN_REFRESH_BUSY_MS = 400

export default function PageRefreshButton({
  onRefreshingChange,
}: {
  onRefreshingChange: (isRefreshing: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const location = useLocation()
  const [spinning, setSpinning] = useState(false)
  const activeRefreshes = useRef(0)
  // 本次刷新批次的起始时刻，用于计算最短展示时长的剩余时间
  const refreshStartedAt = useRef(0)
  // 卸载时清掉旋转定时器，避免测试 teardown 后 setState
  const spinTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  // 最短展示时长的收尾定时器：请求已回但忙碌态还没满 400ms 时，由它负责置回 false
  const busyTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => {
    return () => {
      if (spinTimer.current !== null) {
        clearTimeout(spinTimer.current)
      }
      if (busyTimer.current !== null) {
        clearTimeout(busyTimer.current)
      }
    }
  }, [])

  const refresh = async () => {
    if (spinTimer.current !== null) {
      clearTimeout(spinTimer.current)
      spinTimer.current = null
    }
    // 新一轮刷新开始：取消上一轮可能挂起的收尾，并重置起始时刻
    if (busyTimer.current !== null) {
      clearTimeout(busyTimer.current)
      busyTimer.current = null
    }
    if (activeRefreshes.current === 0) {
      refreshStartedAt.current = Date.now()
      onRefreshingChange(true)
      // 同步通知页面内容区：刷新期间展示加载态（否则点刷新页面毫无变化）
      beginPageRefreshing()
    }
    activeRefreshes.current += 1
    setSpinning(true)
    try {
      const keys = queryKeysForPath(location.pathname)
      if (keys.length === 0) {
        await queryClient.invalidateQueries()
      } else {
        await Promise.all(
          keys.map((queryKey) => queryClient.invalidateQueries({ queryKey })),
        )
      }
    } finally {
      activeRefreshes.current -= 1
      if (activeRefreshes.current === 0) {
        // 不足最短时长则补齐剩余部分，避免忙碌态一闪而过
        const elapsed = Date.now() - refreshStartedAt.current
        const remaining = Math.max(MIN_REFRESH_BUSY_MS - elapsed, 0)
        if (remaining === 0) {
          onRefreshingChange(false)
        } else {
          busyTimer.current = setTimeout(() => {
            busyTimer.current = null
            onRefreshingChange(false)
          }, remaining)
        }
        // 页面内容区的加载态同步收尾（同样带最短窗口）
        endPageRefreshing()
        // 保留短暂的图标旋转反馈；指标忙碌态按上面的最短时长收尾。
        spinTimer.current = setTimeout(() => {
          setSpinning(false)
          spinTimer.current = null
        }, 400)
      }
    }
  }

  return (
    <Button
      type="button"
      size="icon-sm"
      variant="ghost"
      aria-label={t('common.refresh')}
      title={t('common.refresh')}
      data-slot="page-refresh"
      onClick={() => {
        void refresh()
      }}
    >
      <RefreshCw className={spinning ? 'size-4 animate-spin' : 'size-4'} />
    </Button>
  )
}
