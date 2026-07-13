// payload 受控查看弹窗：原因必填（≤255 字）→ POST /admin/v2/messages/{messageId}/payload
// （后端先写审计后返回）→ 展示 payload 原文 + SHA-256 + 大小。
// payload 属敏感瞬态：仅本弹窗内存持有、关闭（卸载）即弃，绝不写日志、不落任何列表。
// 错误（403 无权限 / 400 缺原因等）取 ApiClientError 的脱敏 message 内联展示（ADR-0057，不静默）。
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Label,
  Textarea,
} from '@beacon/ui'

import { viewMessagePayload } from '../../api/connections'
import { ApiClientError } from '../../api/http'

// 查看原因长度上限（与后端 spec §4.4 一致）
const MAX_REASON_LEN = 255

interface PayloadDialogProps {
  // 目标消息 id（调用方按 id 重挂载本组件，草稿与结果不跨消息残留）
  messageId: string
  onClose: () => void
}

export default function PayloadDialog({ messageId, onClose }: PayloadDialogProps) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')

  const mutation = useMutation({
    mutationFn: () => viewMessagePayload(messageId, reason.trim()),
  })
  const result = mutation.data ?? null
  const errorText = mutation.isError
    ? mutation.error instanceof ApiClientError
      ? mutation.error.message
      : String(mutation.error)
    : null
  const canSubmit = reason.trim() !== '' && reason.length <= MAX_REASON_LEN && !mutation.isPending

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) {
          onClose()
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('cluster.topology.payload.title')}</DialogTitle>
          <DialogDescription>{t('cluster.topology.payload.desc')}</DialogDescription>
        </DialogHeader>
        <p className="truncate font-mono text-xs text-ink-3">{messageId}</p>
        {result === null ? (
          <div className="grid gap-1.5">
            <Label htmlFor="payload-view-reason">{t('cluster.topology.payload.reasonLabel')}</Label>
            <Textarea
              id="payload-view-reason"
              rows={3}
              value={reason}
              onChange={(e) => {
                setReason(e.target.value)
              }}
              placeholder={t('cluster.topology.payload.reasonPlaceholder')}
            />
            <p className="text-xs text-muted-foreground tnum">
              {reason.length}/{MAX_REASON_LEN}
            </p>
            {errorText !== null && <p className="text-sm text-destructive">{errorText}</p>}
          </div>
        ) : (
          <div className="grid gap-2">
            {/* payload 原文：限高滚动，长行折行不撑破弹窗 */}
            <pre className="max-h-64 overflow-auto rounded-md bg-muted px-3 py-2 font-mono text-xs break-all whitespace-pre-wrap">
              {result.payload}
            </pre>
            <p className="text-xs text-ink-3">
              <span className="font-semibold">SHA-256</span>{' '}
              <span className="font-mono break-all">{result.sha256}</span>
            </p>
            <p className="text-xs text-ink-3 tnum">
              {t('cluster.topology.payload.sizeBytes', { size: result.size })}
            </p>
          </div>
        )}
        <DialogFooter>
          {result === null ? (
            <Button
              disabled={!canSubmit}
              onClick={() => {
                mutation.mutate()
              }}
            >
              {mutation.isPending
                ? t('cluster.topology.payload.loading')
                : t('cluster.topology.payload.confirm')}
            </Button>
          ) : (
            <Button onClick={onClose}>{t('cluster.topology.payload.close')}</Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
