// 跨服比对面板：输入路径，查看该路径在各子服的哈希分组（同哈希归一组）与缺失该文件的子服。
import { useMemo, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CircleAlert, GitCompare, RefreshCw, Search } from 'lucide-react'

import {
  Badge,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  DataTable,
  Label,
  SectionHeader,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { AssetCompareGroup, AssetCompareResponse } from '@beacon/contracts'

import { ApiClientError } from '../../api/delivery'
import { fetchCompare } from '../../api/delivery-assets'
import AssetPathPicker from '../../features/delivery/asset-path-picker'
import { formatBytes, formatTime, shortHash } from './format'

export default function ComparePanel({ namespaceId }: { namespaceId: number }) {
  const { t } = useTranslation()
  const [path, setPath] = useState('')
  const [result, setResult] = useState<AssetCompareResponse | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  const compareMutation = useMutation({
    mutationFn: (target: string) => fetchCompare({ namespaceId, path: target }),
    onMutate: () => {
      setResult(null)
      setErrorText(null)
    },
    onSuccess: (data) => {
      setResult(data)
      setErrorText(null)
    },
    onError: (error) => {
      setResult(null)
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })
  const columns = useMemo<DataTableColumn<AssetCompareGroup>[]>(
    () => [
      {
        header: t('delivery.assets.compare.columns.digest'),
        cell: (group) => <span className="font-mono text-xs text-ink-2" title={group.sha256}>{shortHash(group.sha256)}</span>,
      },
      {
        header: t('delivery.assets.compare.columns.size'),
        headClassName: 'text-right',
        className: 'text-right tabular-nums',
        cell: (group) => <Badge variant="secondary" className="tnum">{formatBytes(group.size)}</Badge>,
      },
      {
        header: t('delivery.assets.compare.columns.servers'),
        cell: (group) => (
          <div className="flex flex-wrap gap-x-3 gap-y-1.5">
            {group.servers.map((server) => (
              <span key={server.serverId} className="flex items-center gap-1.5">
                <Badge variant="ok" className="font-mono">{server.serverId}</Badge>
                <span className="text-xs text-ink-4">{formatTime(server.scannedAt)}</span>
              </span>
            ))}
          </div>
        ),
      },
    ],
    [t],
  )

  const focusPath = (): void => {
    document.getElementById('compare-path')?.focus()
  }

  return (
    <section className="grid gap-3.5 md:min-h-0 md:flex-1 md:flex md:flex-col" aria-label={t('delivery.assets.compare.title')}>
      <SectionHeader
        icon={<GitCompare className="size-4" aria-hidden />}
        title={t('delivery.assets.compare.title')}
        size="lg"
      />

      <Card className="md:shrink-0">
        <CardHeader className="pb-0">
          <SectionHeader
            icon={<Search className="size-4" aria-hidden />}
            title={t('delivery.assets.compare.formTitle')}
            className="border-0 pb-0"
          />
          <CardDescription>{t('delivery.assets.compare.hint')}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap items-end gap-3">
          <div className="grid w-full max-w-2xl gap-1.5 sm:flex-1">
            <Label htmlFor="compare-path">{t('delivery.assets.compare.pathLabel')}</Label>
            {/* 跨服比对没有"当前服务器"，候选退化为常服目录且仍可手输（比对按全命名空间扫描该路径） */}
            <AssetPathPicker
              id="compare-path"
              aria-label={t('delivery.assets.compare.pathLabel')}
              namespaceId={namespaceId}
              value={path}
              onChange={setPath}
              mode="file"
              placeholder={t('delivery.assets.compare.pathPlaceholder')}
              className="w-full"
            />
          </div>
          <Button
            disabled={path.trim() === '' || compareMutation.isPending}
            onClick={() => {
              compareMutation.mutate(path.trim())
            }}
          >
            <GitCompare className="size-4" aria-hidden />
            {compareMutation.isPending ? t('delivery.assets.compare.loading') : t('delivery.assets.compare.run')}
          </Button>
        </CardContent>
        {/* 禁用态说明：按钮变灰时必须告诉用户"还差什么"，否则只知道点不动 */}
        {path.trim() === '' && (
          <p className="text-xs text-ink-4">{t('delivery.assets.compare.needPath')}</p>
        )}
      </Card>

      {errorText !== null && (
        <Card role="alert" aria-live="polite">
          <CardContent className="flex flex-wrap items-center gap-3 py-3">
            <CircleAlert className="size-4 shrink-0 text-destructive" aria-hidden />
            <div className="grid min-w-0 flex-1 gap-1">
              <CardTitle className="text-sm">{t('delivery.assets.compare.loadFailedTitle')}</CardTitle>
              <CardDescription className="break-words">{errorText}</CardDescription>
            </div>
            <Button
              size="sm"
              variant="outline"
              disabled={compareMutation.isPending || path.trim() === ''}
              onClick={() => { compareMutation.mutate(path.trim()) }}
            >
              <RefreshCw className="size-3.5" aria-hidden />
              {t('delivery.assets.compare.retry')}
            </Button>
          </CardContent>
        </Card>
      )}

      {compareMutation.isPending && (
        <Card>
          <CardHeader className="pb-0">
            <SectionHeader
              title={t('delivery.assets.compare.groupTitle')}
              count={t('delivery.assets.compare.loading')}
              className="border-0 pb-0"
            />
          </CardHeader>
          <CardContent>
            <TableSkeleton columns={3} rows={3} />
          </CardContent>
        </Card>
      )}

      {result === null && errorText === null && !compareMutation.isPending && (
        <Card size="sm">
          <CardContent className="flex flex-wrap items-center justify-center gap-4 py-6">
            <div className="grid size-10 shrink-0 place-items-center rounded-lg bg-brand-50 text-brand">
              <GitCompare className="size-5" aria-hidden />
            </div>
            <div className="grid min-w-0 gap-1 text-center sm:text-left">
              <CardTitle className="text-sm">{t('delivery.assets.compare.emptyTitle')}</CardTitle>
              <CardDescription className="max-w-lg">{t('delivery.assets.compare.emptyDescription')}</CardDescription>
            </div>
            <Button size="sm" variant="outline" onClick={focusPath}>
              <Search className="size-3.5" aria-hidden />
              {t('delivery.assets.compare.emptyAction')}
            </Button>
          </CardContent>
        </Card>
      )}

      {result !== null && (
        <Card className="md:min-h-0 md:flex-1 md:flex md:flex-col">
          <CardHeader className="pb-0 md:shrink-0">
            <SectionHeader
              icon={<GitCompare className="size-4" aria-hidden />}
              title={t('delivery.assets.compare.groupTitle')}
              count={t('delivery.assets.compare.groupCount', { count: result.groups.length })}
              className="border-0 pb-0"
            />
            <CardDescription>
              {t('delivery.assets.compare.resultPath', { path: result.path })}
            </CardDescription>
          </CardHeader>
          <CardContent className="grid gap-4 md:min-h-0 md:flex-1 md:overflow-y-auto">
            {result.groups.length === 0 ? (
              <div className="grid justify-items-center gap-2 py-5 text-center">
                <Search className="size-7 text-ink-4" aria-hidden />
                <CardTitle className="text-sm">{t('delivery.assets.compare.noGroupsTitle')}</CardTitle>
                <CardDescription className="max-w-lg text-center">
                  {t('delivery.assets.compare.noGroupsDescription')}
                </CardDescription>
              </div>
            ) : (
              <DataTable
                columns={columns}
                rows={result.groups}
                rowKey={(group) => group.sha256}
                emptyText={t('delivery.assets.compare.noGroupsTitle')}
                density="compact"
                stickyHeader
              />
            )}

            <div className="grid gap-2 border-t border-border pt-4">
              <SectionHeader
                title={t('delivery.assets.compare.missingTitle')}
                count={t('delivery.assets.compare.missingCount', { count: result.missing.length })}
                className="border-0 pb-0"
              />
              {result.missing.length === 0 ? (
                <Badge variant="ok" className="w-fit">{t('delivery.assets.compare.noMissing')}</Badge>
              ) : (
                <div className="flex flex-wrap gap-1.5">
                  {result.missing.map((serverId) => (
                    <Badge key={serverId} variant="crit" className="gap-1.5 font-mono">
                      <span className="size-1.5 rounded-full bg-current" />
                      {serverId}
                    </Badge>
                  ))}
                </div>
              )}
            </div>
          </CardContent>
        </Card>
      )}
    </section>
  )
}
