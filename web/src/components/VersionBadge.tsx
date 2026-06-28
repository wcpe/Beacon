// 版本徽章（FR-100 / FR-121）：显示控制面版本号，可点击进「版本与更新」页；有可用更新时叠红点。
// FR-121 从 SystemHeader 抽出，置于整宽顶栏品牌区 logo 右侧；版本与更新检查各自走 react-query
// （按 queryKey 去重，与 SystemHeader 同源不重复请求）。

import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { systemStatus } from '@/api/client'
import { useUpdateCheck } from '@/hooks/useUpdateCheck'

// 版本号刷新周期（毫秒）：与 SystemHeader 同 queryKey，react-query 去重，不额外加压
const STATUS_REFETCH_MS = 5000

export default function VersionBadge() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data } = useQuery({
    queryKey: ['system-status'],
    queryFn: systemStatus,
    refetchInterval: STATUS_REFETCH_MS,
  })
  // 更新检查（FR-100）：独立低频 query，仅驱动红点；点击跳「版本与更新」页（ADR-0048）
  const update = useUpdateCheck()
  // 仅 status=ok 且有可用更新、且非 dev 构建时叠红点（check-failed / dev 不提示）
  const hasUpdate = update.data?.status === 'ok' && update.data.hasUpdate && !update.data.isDevBuild
  const version = data?.version ?? '-'

  return (
    <button
      type="button"
      aria-label={t('systemHeader.versionBadgeAria', { version })}
      onClick={() => navigate('/system/version')}
      className="relative shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground transition-colors hover:bg-muted/70 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
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
  )
}
