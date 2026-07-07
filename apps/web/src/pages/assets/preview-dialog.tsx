// 内容预览弹窗：文本文件展示内容；二进制仅元数据；命中敏感规则需先填原因再展示。
// 敏感 403 后弹出原因输入，带原因重试即可查看（原因记入审计）。
import { useEffect, useState } from 'react'
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
  Label,
  ScrollArea,
  Textarea,
} from '@beacon/ui'
import type { AssetPreviewResponse } from '@beacon/devmock'

import { ApiClientError } from '../../api/delivery'
import { previewAsset } from '../../api/delivery-assets'
import { formatBytes, shortHash } from './format'

interface PreviewTarget {
  serverId: string
  path: string
}

interface PreviewDialogProps {
  target: PreviewTarget | null
  onOpenChange: (open: boolean) => void
}

export default function PreviewDialog({ target, onOpenChange }: PreviewDialogProps) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')
  const [needReason, setNeedReason] = useState(false)
  const [result, setResult] = useState<AssetPreviewResponse | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  const previewMutation = useMutation({
    mutationFn: (input: PreviewTarget & { reason?: string }) => previewAsset(input),
    onSuccess: (data) => {
      setResult(data)
      setNeedReason(false)
      setErrorText(null)
    },
    onError: (error) => {
      // 敏感路径未带原因 → 403 asset_sensitive_path，转为要求填原因
      if (error instanceof ApiClientError && error.code === 'asset_sensitive_path') {
        setNeedReason(true)
        setErrorText(null)
      } else {
        setErrorText(error instanceof ApiClientError ? error.message : String(error))
      }
    },
  })

  // 打开时重置并发起首次预览（不带原因，敏感文件会转入原因输入）；仅在 target 变化时触发
  const mutate = previewMutation.mutate
  useEffect(() => {
    if (target) {
      setReason('')
      setNeedReason(false)
      setResult(null)
      setErrorText(null)
      mutate({ serverId: target.serverId, path: target.path })
    }
  }, [target, mutate])

  const submitWithReason = (): void => {
    if (target && reason.trim() !== '') {
      previewMutation.mutate({ serverId: target.serverId, path: target.path, reason: reason.trim() })
    }
  }

  return (
    <Dialog
      open={target !== null}
      onOpenChange={(open) => {
        if (!open) {
          onOpenChange(false)
        }
      }}
    >
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {needReason ? t('delivery.assets.preview.sensitiveTitle') : t('delivery.assets.preview.title')}
          </DialogTitle>
          <DialogDescription className="font-mono text-xs">
            {target ? `${target.serverId} · ${target.path}` : ''}
          </DialogDescription>
        </DialogHeader>

        {/* 敏感文件：填原因后查看 */}
        {needReason && (
          <div className="space-y-2">
            <p className="rounded-md bg-amber-500/10 px-3 py-2 text-sm text-amber-700">
              {t('delivery.assets.preview.sensitiveHint')}
            </p>
            <Label htmlFor="preview-reason">{t('delivery.assets.preview.reasonLabel')}</Label>
            <Textarea
              id="preview-reason"
              aria-label={t('delivery.assets.preview.reasonLabel')}
              value={reason}
              onChange={(e) => {
                setReason(e.target.value)
              }}
              placeholder={t('delivery.assets.preview.reasonPlaceholder')}
              rows={2}
            />
          </div>
        )}

        {/* 内容展示 */}
        {result && !needReason && (
          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <span>
                {t('delivery.assets.preview.meta', {
                  size: formatBytes(result.size),
                  sha256: shortHash(result.sha256),
                })}
              </span>
              {result.sensitive && (
                <Badge variant="destructive">{t('delivery.assets.preview.sensitiveBadge')}</Badge>
              )}
              {result.truncated && (
                <Badge variant="outline">{t('delivery.assets.preview.truncated')}</Badge>
              )}
            </div>
            {result.binary ? (
              <p className="rounded-md bg-muted/50 px-3 py-2 text-sm">
                {t('delivery.assets.preview.binaryOnly')}
              </p>
            ) : (
              <ScrollArea className="h-80 rounded-md border">
                <pre className="whitespace-pre-wrap p-3 font-mono text-xs">{result.content}</pre>
              </ScrollArea>
            )}
          </div>
        )}

        {errorText && <p className="text-sm text-destructive">{errorText}</p>}

        <DialogFooter>
          {needReason && (
            <Button
              disabled={reason.trim() === '' || previewMutation.isPending}
              onClick={submitWithReason}
            >
              {t('delivery.assets.preview.confirm')}
            </Button>
          )}
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('delivery.assets.preview.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
