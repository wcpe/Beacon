// 批量重扫确认弹窗：确认下发重扫命令；返回逐服下发结果（离线服本批跳过）。
import { useTranslation } from 'react-i18next'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  Badge,
} from '@beacon/ui'
import type { AssetRescanResponse } from '@beacon/contracts'

interface RescanDialogProps {
  open: boolean
  serverIds: string[]
  pending: boolean
  result: AssetRescanResponse | null
  errorText: string | null
  onConfirm: () => void
  onOpenChange: (open: boolean) => void
}

export default function RescanDialog({
  open,
  serverIds,
  pending,
  result,
  errorText,
  onConfirm,
  onOpenChange,
}: RescanDialogProps) {
  const { t } = useTranslation()

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!pending) {
          onOpenChange(next)
        }
      }}
    >
      <AlertDialogContent className="grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
        <AlertDialogHeader>
          <AlertDialogTitle>{t('delivery.assets.rescan.title')}</AlertDialogTitle>
          <AlertDialogDescription>{t('delivery.assets.rescan.desc')}</AlertDialogDescription>
        </AlertDialogHeader>

        <div className="min-h-0 overflow-y-auto pr-1">
          <p className="mb-4 text-sm text-ink-3">
            {t('delivery.assets.rescan.selected', { count: serverIds.length })}
          </p>

          {/* 下发结果 */}
          {result && (
            <div className="space-y-1.5">
              <p className="text-sm font-medium text-ink-2">{t('delivery.assets.rescan.resultTitle')}</p>
              <ul className="max-h-48 space-y-1 overflow-y-auto text-sm">
                {result.results.map((r) => (
                  <li key={r.serverId} className="flex items-center justify-between gap-2">
                    <span className="font-mono text-xs text-ink-2">{r.serverId}</span>
                    {r.offline ? (
                      <Badge variant="off" className="gap-1.5">
                        <span className="size-1.5 rounded-full bg-current" />
                        {t('delivery.assets.rescan.offline')}
                      </Badge>
                    ) : (
                      <Badge variant="ok" className="gap-1.5">
                        <span className="size-1.5 rounded-full bg-current" />
                        {t('delivery.assets.rescan.dispatched')}
                      </Badge>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {errorText && <p role="alert" className="mt-3 text-sm text-destructive">{errorText}</p>}
        </div>

        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t('delivery.assets.preview.close')}</AlertDialogCancel>
          {result === null && (
            <AlertDialogAction
              disabled={pending || serverIds.length === 0}
              onClick={(e) => {
                e.preventDefault()
                onConfirm()
              }}
            >
              {t('delivery.assets.rescan.confirm')}
            </AlertDialogAction>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
