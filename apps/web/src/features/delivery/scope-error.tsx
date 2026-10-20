import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'

interface DeliveryScopeErrorProps {
  error: unknown
  onRetry: () => void
}

export default function DeliveryScopeError({ error, onRetry }: DeliveryScopeErrorProps) {
  const { t } = useTranslation()
  const message = error instanceof Error ? error.message : String(error)

  return (
    <div
      data-slot="delivery-scope-error"
      role="alert"
      className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-surface-2/40 px-4 py-3"
    >
      <p className="min-w-0 flex-1 text-sm text-destructive">
        {t('delivery.scope.loadError', { message })}
      </p>
      <Button type="button" variant="outline" size="sm" onClick={onRetry}>
        {t('delivery.scope.retry')}
      </Button>
    </div>
  )
}
