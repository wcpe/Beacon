// 更新进度卡片：阶段 / 百分比 / 目标版本 + 取消（进行中）+ 失败原因。
// 对齐 B 版——区段图标标题 + 阶段状态药丸 + 语义色进度条。
import { useTranslation } from 'react-i18next'
import { DownloadCloud } from 'lucide-react'

import { Badge, Button, SectionHeader, cn } from '@beacon/ui'
import type { UpdateProgress } from '@beacon/devmock'

interface ProgressCardProps {
  progress: UpdateProgress
  cancelling: boolean
  onCancel: () => void
}

// 阶段 → i18n 键后缀
function phaseSuffix(phase: string): string {
  const camel = phase.replace(/-([a-z])/g, (_, c: string) => c.toUpperCase())
  return camel.charAt(0).toUpperCase() + camel.slice(1)
}

// 进行中阶段（可取消）
const CANCELLABLE = new Set(['downloading', 'verifying', 'staging'])

export default function ProgressCard({ progress, cancelling, onCancel }: ProgressCardProps) {
  const { t } = useTranslation()
  const inProgress = progress.phase !== 'idle'
  const cancellable = CANCELLABLE.has(progress.phase)
  const failed = progress.phase === 'failed'

  if (!inProgress) {
    return null
  }

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<DownloadCloud className="size-4" />}
        title={t('system.version.progress.title')}
        actions={
          cancellable && (
            <Button size="sm" variant="outline" disabled={cancelling} onClick={onCancel}>
              {t('system.version.cancel')}
            </Button>
          )
        }
      />
      <div className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <Badge variant={failed ? 'crit' : 'brand'} className="gap-1.5">
            <span className="size-1.5 rounded-full bg-current" />
            {t(`system.version.progress.${lowerFirst(phaseSuffix(progress.phase))}`)}
          </Badge>
          {progress.targetVersion !== '' && (
            <span className="text-ink-3">
              {t('system.version.progress.target')}: {progress.targetVersion}
            </span>
          )}
          <span className="ml-auto text-[15px] font-semibold text-ink-1 tnum">{progress.percent}%</span>
        </div>
        {/* 进度条 */}
        <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
          <div
            className={cn('h-full rounded-full transition-all', failed ? 'bg-crit' : 'bg-brand')}
            style={{ width: `${String(progress.percent)}%` }}
          />
        </div>
        {progress.error !== '' && <p className="text-sm text-crit">{progress.error}</p>}
      </div>
    </section>
  )
}

function lowerFirst(value: string): string {
  return value.charAt(0).toLowerCase() + value.slice(1)
}
