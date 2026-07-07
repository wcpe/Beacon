// 跨服比对面板：输入路径，查看该路径在各子服的哈希分组（同哈希归一组）与缺失该文件的子服。
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Badge, Button, Input, Label, SectionHeader } from '@beacon/ui'
import type { AssetCompareResponse } from '@beacon/devmock'

import { ApiClientError } from '../../api/delivery'
import { fetchCompare } from '../../api/delivery-assets'
import { formatBytes, formatTime, shortHash } from './format'

export default function ComparePanel({ namespaceId }: { namespaceId: number }) {
  const { t } = useTranslation()
  const [path, setPath] = useState('')
  const [result, setResult] = useState<AssetCompareResponse | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  const compareMutation = useMutation({
    mutationFn: (target: string) => fetchCompare({ namespaceId, path: target }),
    onSuccess: (data) => {
      setResult(data)
      setErrorText(null)
    },
    onError: (error) => {
      setResult(null)
      setErrorText(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  return (
    <section className="grid gap-3" aria-label={t('delivery.assets.compare.title')}>
      <SectionHeader title={t('delivery.assets.compare.title')} />
      <p className="text-sm text-muted-foreground">{t('delivery.assets.compare.hint')}</p>

      <div className="flex flex-wrap items-end gap-2">
        <div className="grid gap-1.5">
          <Label htmlFor="compare-path">{t('delivery.assets.compare.pathLabel')}</Label>
          <Input
            id="compare-path"
            aria-label={t('delivery.assets.compare.pathLabel')}
            placeholder={t('delivery.assets.compare.pathPlaceholder')}
            value={path}
            onChange={(e) => {
              setPath(e.target.value)
            }}
            className="w-96"
          />
        </div>
        <Button
          disabled={path.trim() === '' || compareMutation.isPending}
          onClick={() => {
            compareMutation.mutate(path.trim())
          }}
        >
          {t('delivery.assets.compare.run')}
        </Button>
      </div>

      {errorText && <p className="text-sm text-destructive">{errorText}</p>}

      {result === null && !errorText && (
        <p className="text-sm text-muted-foreground">{t('delivery.assets.compare.empty')}</p>
      )}

      {result && (
        <div className="grid gap-4">
          {/* 哈希分组 */}
          <div className="grid gap-2">
            <p className="text-sm font-medium">{t('delivery.assets.compare.groupTitle')}</p>
            {result.groups.map((group) => (
              <div key={group.sha256} className="rounded-md border p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-xs">{shortHash(group.sha256)}</span>
                  <Badge variant="secondary">{formatBytes(group.size)}</Badge>
                  <Badge variant="outline">
                    {t('delivery.assets.compare.groupServers', { count: group.servers.length })}
                  </Badge>
                </div>
                <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                  {group.servers.map((s) => (
                    <li key={s.serverId} className="font-mono">
                      {s.serverId}
                      <span className="ml-1 opacity-70">{formatTime(s.scannedAt)}</span>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </div>

          {/* 缺失该文件的子服 */}
          <div className="grid gap-2">
            <p className="text-sm font-medium">{t('delivery.assets.compare.missingTitle')}</p>
            {result.missing.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t('delivery.assets.compare.noMissing')}</p>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {result.missing.map((serverId) => (
                  <Badge key={serverId} variant="destructive" className="font-mono">
                    {serverId}
                  </Badge>
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </section>
  )
}
