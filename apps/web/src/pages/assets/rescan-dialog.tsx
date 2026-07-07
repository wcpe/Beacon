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
import type { AssetRescanResponse } from '@beacon/devmock'

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
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t('delivery.assets.rescan.title')}</AlertDialogTitle>
          <AlertDialogDescription>{t('delivery.assets.rescan.desc')}</AlertDialogDescription>
        </AlertDialogHeader>

        <p className="text-sm text-muted-foreground">
          {t('delivery.assets.rescan.selected', { count: serverIds.length })}
        </p>

        {/* 下发结果 */}
        {result && (
          <div className="space-y-1.5">
            <p className="text-sm font-medium">{t('delivery.assets.rescan.resultTitle')}</p>
            <ul className="max-h-48 space-y-1 overflow-y-auto text-sm">
              {result.results.map((r) => (
                <li key={r.serverId} className="flex items-center justify-between gap-2">
                  <span className="font-mono text-xs">{r.serverId}</span>
                  {r.offline ? (
                    <Badge variant="outline">{t('delivery.assets.rescan.offline')}</Badge>
                  ) : (
                    <Badge variant="secondary">{t('delivery.assets.rescan.dispatched')}</Badge>
                  )}
                </li>
              ))}
            </ul>
          </div>
        )}

        {errorText && <p className="text-sm text-destructive">{errorText}</p>}

        <AlertDialogFooter>
          <AlertDialogCancel>{t('delivery.assets.preview.close')}</AlertDialogCancel>
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
