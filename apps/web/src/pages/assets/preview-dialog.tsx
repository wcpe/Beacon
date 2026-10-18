import { useEffect } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  Badge,
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@beacon/ui'

import type { AssetPreviewResponse } from '@beacon/contracts'
import { ApiClientError } from '../../api/http'
import { previewAsset } from '../../api/delivery-assets'
import { formatBytes, shortHash } from './format'

interface PreviewTarget {
  namespaceId: number
  serverId: string
  path: string
}

interface PreviewDialogProps {
  target: PreviewTarget | null
  onOpenChange: (open: boolean) => void
}

const PREVIEW_REASON = '管理台文件正文直读'

function errorMessage(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function PreviewDialog({ target, onOpenChange }: PreviewDialogProps) {
  const { t } = useTranslation()
  const previewMutation = useMutation<AssetPreviewResponse>({
    mutationFn: () => {
      if (target === null) return Promise.reject(new Error('未选择文件'))
      return previewAsset(target, PREVIEW_REASON)
    },
  })
  const handleOpenChange = (open: boolean): void => {
    if (!open) previewMutation.reset()
    onOpenChange(open)
  }

  // 打开即读取：弹窗本身就是"查看正文"这一步，不再额外要求用户二次确认。
  // 审计仍由后端在返回正文前记录，前端无需为此加一道点击。
  const mutate = previewMutation.mutate
  const namespaceId = target?.namespaceId
  const serverId = target?.serverId
  const path = target?.path
  useEffect(() => {
    if (namespaceId === undefined || serverId === undefined || path === undefined) {
      return
    }
    mutate()
  }, [namespaceId, serverId, path, mutate])

  if (target === null) return null

  return (
    <Dialog open onOpenChange={handleOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t('delivery.assets.preview.title')}</DialogTitle>
          <DialogDescription className="break-all font-mono">
            {target.serverId}:{target.path}
          </DialogDescription>
        </DialogHeader>

        {previewMutation.data && (
          <div className="grid gap-3">
            <div className="flex flex-wrap items-center gap-2 text-xs text-ink-3">
              <span>{t('delivery.assets.preview.meta', {
                size: formatBytes(previewMutation.data.size),
                sha256: shortHash(previewMutation.data.sha256),
              })}</span>
              {previewMutation.data.truncated && <span>{t('delivery.assets.preview.truncated')}</span>}
            </div>
            {previewMutation.data.sensitive && (
              // 只保留「敏感」标记本身：审计照常记录，不需要在正文旁再解释一遍流程
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="warn">{t('delivery.assets.preview.sensitiveBadge')}</Badge>
              </div>
            )}
            {previewMutation.data.binary ? (
              <p className="text-sm text-ink-3">{t('delivery.assets.preview.binaryOnly')}</p>
            ) : (
              <pre className="max-h-[50vh] overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-surface-2 p-3 font-mono text-xs leading-relaxed text-ink-2">
                {previewMutation.data.content ?? ''}
              </pre>
            )}
          </div>
        )}

        {/* 打开即读取：加载中直接给读取态，不再有"再点一次确认"的按钮 */}
        {previewMutation.isPending && (
          <p className="text-sm text-ink-3">{t('delivery.assets.detail.previewLoading')}</p>
        )}

        {previewMutation.isError && (
          <div className="grid gap-2" role="alert" aria-live="polite">
            <p className="text-sm text-destructive">{errorMessage(previewMutation.error)}</p>
            <Button
              type="button"
              variant="outline"
              className="w-fit"
              disabled={previewMutation.isPending}
              onClick={() => { previewMutation.mutate() }}
            >
              {t('delivery.assets.preview.retry')}
            </Button>
          </div>
        )}

        {previewMutation.data && (
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { handleOpenChange(false) }}>
              {t('delivery.assets.preview.close')}
            </Button>
          </DialogFooter>
        )}
      </DialogContent>
    </Dialog>
  )
}
