// 两侧 diff 面板：选两台子服的同名文件做行级比对；哈希相同则提示一致；敏感命中需原因。
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { SplitSquareHorizontal } from 'lucide-react'

import { Button, Input, Label, SectionHeader, Textarea } from '@beacon/ui'
import type { AssetDiffResponse } from '@beacon/contracts'

import TextDiff from '../../features/delivery/text-diff'
import { ApiClientError } from '../../api/delivery'
import { diffAssets } from '../../api/delivery-assets'

export default function DiffPanel() {
  const { t } = useTranslation()
  const [leftServer, setLeftServer] = useState('')
  const [rightServer, setRightServer] = useState('')
  const [path, setPath] = useState('')
  const [reason, setReason] = useState('')
  const [needReason, setNeedReason] = useState(false)
  const [result, setResult] = useState<AssetDiffResponse | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  const diffMutation = useMutation({
    mutationFn: (withReason: string | undefined) =>
      diffAssets({
        left: { serverId: leftServer.trim(), path: path.trim() },
        right: { serverId: rightServer.trim(), path: path.trim() },
        reason: withReason,
      }),
    onSuccess: (data) => {
      setResult(data)
      setErrorText(null)
      setNeedReason(false)
    },
    onError: (error) => {
      setResult(null)
      // 任一侧命中敏感规则且未带原因 → 403 asset_sensitive_path，转为要求填原因
      if (error instanceof ApiClientError && error.code === 'asset_sensitive_path') {
        setNeedReason(true)
        setErrorText(null)
      } else {
        setErrorText(error instanceof ApiClientError ? error.message : String(error))
      }
    },
  })

  const canRun =
    leftServer.trim() !== '' && rightServer.trim() !== '' && path.trim() !== '' && !diffMutation.isPending

  return (
    <section className="grid gap-3" aria-label={t('delivery.assets.diff.title')}>
      <SectionHeader
        icon={<SplitSquareHorizontal className="size-4" aria-hidden />}
        title={t('delivery.assets.diff.title')}
      />
      <p className="text-sm text-ink-3">{t('delivery.assets.diff.hint')}</p>

      <div className="flex flex-wrap items-end gap-2">
        <div className="grid gap-1.5">
          <Label htmlFor="diff-left">{t('delivery.assets.diff.leftServer')}</Label>
          <Input
            id="diff-left"
            aria-label={t('delivery.assets.diff.leftServer')}
            value={leftServer}
            onChange={(e) => {
              setLeftServer(e.target.value)
            }}
            className="w-40 font-mono"
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="diff-right">{t('delivery.assets.diff.rightServer')}</Label>
          <Input
            id="diff-right"
            aria-label={t('delivery.assets.diff.rightServer')}
            value={rightServer}
            onChange={(e) => {
              setRightServer(e.target.value)
            }}
            className="w-40 font-mono"
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="diff-path">{t('delivery.assets.diff.pathLabel')}</Label>
          <Input
            id="diff-path"
            aria-label={t('delivery.assets.diff.pathLabel')}
            value={path}
            onChange={(e) => {
              setPath(e.target.value)
            }}
            className="w-80 font-mono"
          />
        </div>
        <Button
          disabled={!canRun}
          onClick={() => {
            setReason('')
            setNeedReason(false)
            diffMutation.mutate(undefined)
          }}
        >
          {t('delivery.assets.diff.run')}
        </Button>
      </div>

      {/* 敏感命中：填原因后带原因重试 */}
      {needReason && (
        <div className="space-y-2">
          <p className="rounded-lg border border-warn-bd bg-warn-bg px-3 py-2 text-sm text-warn">
            {t('delivery.assets.diff.sensitiveHint')}
          </p>
          <Label htmlFor="diff-reason">{t('delivery.assets.diff.reasonLabel')}</Label>
          <Textarea
            id="diff-reason"
            aria-label={t('delivery.assets.diff.reasonLabel')}
            value={reason}
            onChange={(e) => {
              setReason(e.target.value)
            }}
            placeholder={t('delivery.assets.diff.reasonPlaceholder')}
            rows={2}
          />
          <Button
            disabled={reason.trim() === '' || diffMutation.isPending}
            onClick={() => {
              diffMutation.mutate(reason.trim())
            }}
          >
            {t('delivery.assets.diff.confirm')}
          </Button>
        </div>
      )}

      {errorText && <p className="text-sm text-destructive">{errorText}</p>}

      {result === null && !errorText && !needReason && (
        <p className="text-sm text-ink-3">{t('delivery.assets.diff.empty')}</p>
      )}

      {result?.identical === true && (
        <p className="flex items-center gap-2 rounded-lg border border-ok-bd bg-ok-bg px-3 py-2 text-sm text-ok">
          <span className="size-1.5 rounded-full bg-current" />
          {t('delivery.assets.diff.identical')}
        </p>
      )}

      {result && !result.identical && result.left && result.right && (
        <TextDiff
          left={result.left.content}
          right={result.right.content}
          leftLabel={`${t('delivery.assets.diff.leftHeading')} · ${result.left.serverId}`}
          rightLabel={`${t('delivery.assets.diff.rightHeading')} · ${result.right.serverId}`}
        />
      )}
    </section>
  )
}
