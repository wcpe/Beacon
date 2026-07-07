// 更新进度卡片：阶段 / 百分比 / 目标版本 + 取消（进行中）+ 失败原因展示。
import { useTranslation } from 'react-i18next'

import { Badge, Button, Card, CardContent, CardHeader, CardTitle } from '@beacon/ui'
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

  if (!inProgress) {
    return null
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="text-base">{t('system.version.progress.title')}</CardTitle>
        {cancellable && (
          <Button size="sm" variant="outline" disabled={cancelling} onClick={onCancel}>
            {t('system.version.cancel')}
          </Button>
        )}
      </CardHeader>
      <CardContent className="grid gap-3">
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <Badge variant={progress.phase === 'failed' ? 'destructive' : 'secondary'}>
            {t(`system.version.progress.${lowerFirst(phaseSuffix(progress.phase))}`)}
          </Badge>
          {progress.targetVersion !== '' && (
            <span className="text-muted-foreground">
              {t('system.version.progress.target')}: {progress.targetVersion}
            </span>
          )}
          <span className="tabular-nums">{progress.percent}%</span>
        </div>
        {/* 进度条 */}
        <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
          <div
            className={progress.phase === 'failed' ? 'h-full bg-destructive' : 'h-full bg-primary'}
            style={{ width: `${String(progress.percent)}%` }}
          />
        </div>
        {progress.error !== '' && <p className="text-sm text-destructive">{progress.error}</p>}
      </CardContent>
    </Card>
  )
}

function lowerFirst(value: string): string {
  return value.charAt(0).toLowerCase() + value.slice(1)
}
