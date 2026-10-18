// 两侧差异面板：直读并展示服务端返回的文本差异，读取行为由后端记入审计。
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CircleAlert, RefreshCw, SplitSquareHorizontal } from 'lucide-react'

import {
  Badge,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
  Label,
  SectionHeader,
} from '@beacon/ui'

import AssetPathPicker from '../../features/delivery/asset-path-picker'
import ServerPicker from '../../features/delivery/server-picker'
import { readAssetPair, type AssetPairDiffResponse } from '../../api/delivery-assets'
import { ApiClientError } from '../../api/http'
import { shortHash } from './format'

function errorMessage(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

const DIFF_REASON = '管理台文件差异直读'

export default function DiffPanel({ namespaceId }: { namespaceId: number }) {
  const { t } = useTranslation()
  const [leftServer, setLeftServer] = useState('')
  const [rightServer, setRightServer] = useState('')
  const [path, setPath] = useState('')
  const [result, setResult] = useState<AssetPairDiffResponse | null>(null)
  const canCompare = leftServer.trim() !== '' && rightServer.trim() !== '' && path.trim() !== ''

  const diffMutation = useMutation({
    mutationFn: () => readAssetPair(
      { namespaceId, serverId: leftServer.trim(), path: path.trim() },
      { namespaceId, serverId: rightServer.trim(), path: path.trim() },
      DIFF_REASON,
    ),
    onSuccess: setResult,
  })
  const error = diffMutation.error

  const compare = (): void => {
    setResult(null)
    diffMutation.reset()
    diffMutation.mutate()
  }

  const retryError = (): void => {
    diffMutation.mutate()
  }

  return (
    <section className="grid gap-3.5" aria-label={t('delivery.assets.diff.title')}>
      <SectionHeader
        icon={<SplitSquareHorizontal className="size-4" aria-hidden />}
        title={t('delivery.assets.diff.title')}
        size="lg"
      />

      <Card>
        <CardHeader className="pb-0">
          <SectionHeader
            icon={<SplitSquareHorizontal className="size-4" aria-hidden />}
            title={t('delivery.assets.diff.formTitle')}
            className="border-0 pb-0"
          />
          <CardDescription>{t('delivery.assets.diff.hint')}</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
            <div className="grid min-w-0 gap-1.5">
              <Label htmlFor="diff-left">{t('delivery.assets.diff.leftServer')}</Label>
              {/* 两台服的同名文件比对：候选为有真实清单的后端服，严格选避免手填拼错 */}
              <ServerPicker
                id="diff-left"
                aria-label={t('delivery.assets.diff.leftServer')}
                namespaceId={namespaceId}
                value={leftServer}
                onChange={setLeftServer}
                className="w-full"
              />
            </div>
            <div className="grid min-w-0 gap-1.5">
              <Label htmlFor="diff-right">{t('delivery.assets.diff.rightServer')}</Label>
              <ServerPicker
                id="diff-right"
                aria-label={t('delivery.assets.diff.rightServer')}
                namespaceId={namespaceId}
                value={rightServer}
                onChange={setRightServer}
                className="w-full"
              />
            </div>
            <div className="grid min-w-0 gap-1.5 md:col-span-2 xl:col-span-1">
              <Label htmlFor="diff-path">{t('delivery.assets.diff.pathLabel')}</Label>
              {/* 路径随左侧服级联：选定左侧服后列出该服真实文件路径，清单未覆盖的仍可手输 */}
              <AssetPathPicker
                id="diff-path"
                aria-label={t('delivery.assets.diff.pathLabel')}
                namespaceId={namespaceId}
                serverId={leftServer}
                value={path}
                onChange={setPath}
                mode="file"
                className="w-full"
              />
            </div>
          </div>
        </CardContent>
        <CardFooter className="flex flex-wrap items-center justify-between gap-3">
          {/* 只在按钮不可用时说明缺什么；可比对时不再展示流程解释（审计照常记录） */}
          <CardDescription className="min-w-0 flex-1">
            {!canCompare ? t('delivery.assets.diff.needBothSides') : ''}
          </CardDescription>
          <Button variant="default" disabled={!canCompare || diffMutation.isPending} onClick={compare}>
            {diffMutation.isPending ? t('delivery.assets.diff.loading') : t('delivery.assets.diff.run')}
          </Button>
        </CardFooter>
      </Card>

      {error && (
        <Card role="alert" aria-live="polite">
          <CardContent className="flex flex-wrap items-center gap-3 py-3">
            <CircleAlert className="size-4 shrink-0 text-destructive" aria-hidden />
            <div className="grid min-w-0 flex-1 gap-1">
              <CardTitle className="text-sm">{t('delivery.assets.diff.errorTitle')}</CardTitle>
              <CardDescription className="break-words">{errorMessage(error)}</CardDescription>
            </div>
            <Button
              size="sm"
              variant="outline"
              disabled={diffMutation.isPending}
              onClick={retryError}
            >
              <RefreshCw className="size-3.5" aria-hidden />
              {t('delivery.assets.diff.retry')}
            </Button>
          </CardContent>
        </Card>
      )}

      {result === null && !error && (
        <Card size="sm">
          <CardContent className="flex items-start gap-3 py-4">
            <SplitSquareHorizontal className="mt-0.5 size-5 shrink-0 text-brand" aria-hidden />
            <div className="grid gap-1">
              <CardTitle className="text-sm">{t('delivery.assets.diff.emptyTitle')}</CardTitle>
              <CardDescription>{t('delivery.assets.diff.emptyDescription')}</CardDescription>
            </div>
          </CardContent>
        </Card>
      )}

      {result !== null && <DiffResult result={result} />}
    </section>
  )
}

function DiffResult({ result }: { result: AssetPairDiffResponse }) {
  const { t } = useTranslation()
  const left = result.left
  const right = result.right
  return (
    <Card aria-label={t('delivery.assets.diff.summaryTitle')}>
      <CardHeader className="pb-0">
        <SectionHeader
          title={t('delivery.assets.diff.summaryTitle')}
          className="border-0 pb-0"
        />
        <CardDescription>{t('delivery.assets.diff.summaryHint')}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <Badge className="w-fit" variant={result.identical ? 'ok' : 'warn'}>
          {result.identical ? t('delivery.assets.diff.identical') : t('delivery.assets.diff.changed')}
        </Badge>
        {result.identical ? (
          <p className="text-sm text-ink-3">{t('delivery.assets.diff.identicalHint')}</p>
        ) : left && right ? (
          <div className="grid min-w-0 gap-3 md:grid-cols-2">
            <DiffSide label={t('delivery.assets.diff.leftHeading')} side={left} />
            <DiffSide label={t('delivery.assets.diff.rightHeading')} side={right} />
          </div>
        ) : (
          <p className="text-sm text-destructive">{t('delivery.assets.diff.contentUnavailable')}</p>
        )}
      </CardContent>
    </Card>
  )
}

function DiffSide({
  label,
  side,
}: {
  label: string
  side: NonNullable<AssetPairDiffResponse['left']>
}) {
  const { t } = useTranslation()
  return (
    <section className="grid min-w-0 gap-2 rounded-lg border border-border">
      <div className="grid gap-1 border-b border-border bg-surface-2 px-3 py-2 text-xs">
        <span className="font-medium text-ink-1">{label} · {side.serverId}</span>
        <span className="break-all font-mono text-ink-3">{side.path}</span>
        <span className="text-ink-3">{t('delivery.assets.diff.sideHash', { sha256: shortHash(side.sha256) })}</span>
      </div>
      <pre className="max-h-[50vh] overflow-auto whitespace-pre-wrap break-words px-3 pb-3 font-mono text-xs leading-relaxed text-ink-2">
        {side.content}
      </pre>
    </section>
  )
}
