// 版本内容查看弹窗：GET config-versions/:versionId 拉取脱敏内容只读展示。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@beacon/ui'

import { fetchConfigVersion } from '../../api/delivery-configs'

interface VersionViewDialogProps {
  versionId: number
  onOpenChange: (open: boolean) => void
}

export default function VersionViewDialog({ versionId, onOpenChange }: VersionViewDialogProps) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['configs', 'version', versionId],
    queryFn: () => fetchConfigVersion(versionId),
  })

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) {
          onOpenChange(false)
        }
      }}
    >
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('delivery.configs.detail.versions.view')}</DialogTitle>
        </DialogHeader>
        <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
          {query.data && (
            <pre className="max-h-[60vh] overflow-auto rounded-md border bg-muted/30 p-3 font-mono text-xs whitespace-pre-wrap">
              {query.data.content === '' ? '(空)' : query.data.content}
            </pre>
          )}
        </AsyncSection>
      </DialogContent>
    </Dialog>
  )
}
