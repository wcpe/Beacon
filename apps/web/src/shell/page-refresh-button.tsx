// 页眉刷新当前页（FR-196）：按路由前缀 invalidate react-query，不整页 reload。
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useLocation } from 'react-router-dom'
import { RefreshCw } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { Button } from '@beacon/ui'

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

export default function PageRefreshButton() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const location = useLocation()
  const [spinning, setSpinning] = useState(false)
  // 卸载时清掉旋转定时器，避免测试 teardown 后 setState
  const spinTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => {
    return () => {
      if (spinTimer.current !== null) {
        clearTimeout(spinTimer.current)
      }
    }
  }, [])

  const refresh = async () => {
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
      // 短延迟让旋转可见
      if (spinTimer.current !== null) {
        clearTimeout(spinTimer.current)
      }
      spinTimer.current = setTimeout(() => {
        setSpinning(false)
        spinTimer.current = null
      }, 400)
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
