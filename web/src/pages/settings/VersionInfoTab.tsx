// 系统信息块「版本与更新」子 tab（FR-100，接入 FR-99）：
// 在设置聚合页内呈现当前版本 / 渠道 / 更新状态，并提供打开更新模态框的入口
// （与页眉版本徽章同一 UpdateModal、同一 useUpdateCheck 低频检查）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Download } from 'lucide-react'

import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import UpdateModal from '@/components/UpdateModal'
import { useUpdateCheck } from '@/hooks/useUpdateCheck'

export default function VersionInfoTab() {
  const { t } = useTranslation()
  const update = useUpdateCheck()
  const [open, setOpen] = useState(false)

  const data = update.data
  const hasUpdate = data?.status === 'ok' && data.hasUpdate && !data.isDevBuild

  // 状态行：与模态框口径一致
  function statusLine(): string {
    if (update.isLoading || !data) return t('updateModal.checking')
    if (update.isError) return t('updateModal.checkFailed')
    if (data.isDevBuild) return t('updateModal.devBuild')
    if (data.status === 'check-failed') return t('updateModal.checkFailed')
    if (data.hasUpdate) return t('updateModal.hasUpdate', { version: data.latestVersion })
    return t('updateModal.upToDate')
  }

  return (
    <Card>
      <CardContent className="space-y-4 py-6">
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
          <dt className="text-muted-foreground">{t('updateModal.currentVersion')}</dt>
          <dd className="font-medium tabular-nums">{data?.currentVersion ?? '-'}</dd>
          {data?.channel && (
            <>
              <dt className="text-muted-foreground">{t('updateModal.channel')}</dt>
              <dd className="font-medium">{data.channel}</dd>
            </>
          )}
        </dl>

        <div className="flex items-center gap-3">
          <span className={hasUpdate ? 'text-sm font-medium text-foreground' : 'text-sm text-muted-foreground'}>
            {statusLine()}
          </span>
          <Button size="sm" variant={hasUpdate ? 'default' : 'outline'} onClick={() => setOpen(true)}>
            <Download />
            {hasUpdate ? t('updateModal.updateNow') : t('updateModal.checkNow')}
          </Button>
        </div>
      </CardContent>

      <UpdateModal
        open={open}
        onOpenChange={setOpen}
        data={update.data}
        isLoading={update.isLoading}
        isError={update.isError}
        onRefresh={update.refresh}
      />
    </Card>
  )
}
