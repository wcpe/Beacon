import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button, Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, Label, Textarea } from '@beacon/ui'

import { readMessagePayload } from '../../api/connections'
import { ApiClientError } from '../../api/http'

interface PayloadDialogProps {
  messageId: string
  onClose: () => void
}

function errorMessage(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function PayloadDialog({ messageId, onClose }: PayloadDialogProps) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')
  const payloadMutation = useMutation({
    mutationFn: () => readMessagePayload(messageId, reason.trim()),
  })
  const close = (): void => {
    if (payloadMutation.isPending) return
    setReason('')
    payloadMutation.reset()
    onClose()
  }
  const canRead = reason.trim() !== '' && reason.trim().length <= 255 && !payloadMutation.isPending
  const result = payloadMutation.data

  return (
    <Dialog open onOpenChange={(open) => { if (!open) close() }}>
      <DialogContent
        className="grid-rows-[auto_minmax(0,1fr)_auto] max-h-[calc(100dvh-2rem)] overflow-hidden"
        showCloseButton={!payloadMutation.isPending}
        onInteractOutside={(event) => { event.preventDefault() }}
      >
        <DialogHeader>
          <DialogTitle>{t('cluster.topology.payload.title')}</DialogTitle>
          <DialogDescription>{t('cluster.topology.payload.desc')}</DialogDescription>
        </DialogHeader>
        <div className="min-h-0 overflow-y-auto pr-1">
          <p className="mb-4 break-all rounded-md bg-surface-2 px-3 py-2 font-mono text-xs text-ink-3">
            {messageId}
          </p>
          {result === undefined ? (
            <div className="grid gap-1.5">
              <Label htmlFor="message-payload-reason">{t('cluster.topology.payload.reasonLabel')}</Label>
              <Textarea
                id="message-payload-reason"
                value={reason}
                maxLength={255}
                onChange={(event) => { setReason(event.target.value) }}
                placeholder={t('cluster.topology.payload.reasonPlaceholder')}
                rows={3}
              />
            </div>
          ) : (
            <div className="grid gap-3">
              <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">
                <span>{t('cluster.topology.payload.sizeBytes', { size: result.size })}</span>
                <span>{t('cluster.topology.payload.sha256')}: <code className="break-all">{result.sha256}</code></span>
              </div>
              <pre className="max-h-[50vh] overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-surface-2 p-3 font-mono text-xs leading-relaxed text-ink-2">
                {result.payload}
              </pre>
            </div>
          )}
          {payloadMutation.isPending && (
            <p className="mt-3 text-sm text-ink-3">{t('cluster.topology.payload.loading')}</p>
          )}
          {payloadMutation.isError && (
            <p className="mt-3 text-sm text-destructive" role="alert">{errorMessage(payloadMutation.error)}</p>
          )}
        </div>
        <DialogFooter>
          {result === undefined && (
            <Button
              type="button"
              disabled={!canRead}
              onClick={() => { payloadMutation.mutate() }}
            >
              {payloadMutation.isPending ? t('cluster.topology.payload.loading') : t('cluster.topology.payload.confirm')}
            </Button>
          )}
          <Button type="button" variant="outline" disabled={payloadMutation.isPending} onClick={close}>
            {t('cluster.topology.payload.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
