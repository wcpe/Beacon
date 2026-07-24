// 文件清单详情（右侧非模态详情面板内容）：元数据（子服 / 路径 / 大小 / 类型 / 哈希 / 修改时间）+ 内容预览。
// 基础元数据取自列表行数据、无需再取数即时展示；内容预览自动取数内联渲染。
// 敏感文件命中 403 时保留「填写原因」模态（PreviewDialog）——那是表单，符合模态用于写/确认的约定。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Badge, Button, ScrollArea } from '@beacon/ui'
import type { AssetItem } from '@beacon/contracts'

import { ApiClientError } from '../../api/delivery'
import { previewAsset } from '../../api/delivery-assets'
import PreviewDialog from './preview-dialog'
import { formatBytes, formatTime, shortHash } from './format'

// 元数据行：标签 + 值
function MetaRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[5rem_minmax(0,1fr)] items-baseline gap-2 text-xs">
      <span className="text-ink-4">{label}</span>
      <span className="min-w-0 break-all text-ink-2">{children}</span>
    </div>
  )
}

export default function ManifestDetailPanel({ item }: { item: AssetItem }) {
  const { t } = useTranslation()
  // 敏感文件填原因的模态目标（仅敏感命中时打开）
  const [reasonTarget, setReasonTarget] = useState<{ serverId: string; path: string } | null>(null)

  const previewQuery = useQuery({
    queryKey: ['assets', 'preview', item.serverId, item.path],
    queryFn: () => previewAsset({ serverId: item.serverId, path: item.path }),
    retry: false,
  })

  // 敏感命中：后端返回 403 asset_sensitive_path，转为「填原因」引导而非报错
  const sensitiveBlocked =
    previewQuery.error instanceof ApiClientError &&
    previewQuery.error.code === 'asset_sensitive_path'
  const otherError =
    previewQuery.isError && !sensitiveBlocked
      ? previewQuery.error instanceof ApiClientError
        ? previewQuery.error.message
        : String(previewQuery.error)
      : null
  const result = previewQuery.data ?? null

  return (
    <div className="grid gap-4">
      {/* 元数据（前置，取自列表行、无需取数） */}
      <section className="grid gap-1.5">
        <h4 className="text-[13px] font-semibold text-ink-1">{t('delivery.assets.detail.metaTitle')}</h4>
        <MetaRow label={t('delivery.assets.detail.fields.serverId')}>
          <span className="font-mono">{item.serverId}</span>
        </MetaRow>
        <MetaRow label={t('delivery.assets.detail.fields.path')}>
          <span className="font-mono">{item.path}</span>
        </MetaRow>
        <MetaRow label={t('delivery.assets.detail.fields.size')}>{formatBytes(item.size)}</MetaRow>
        <MetaRow label={t('delivery.assets.detail.fields.type')}>
          {item.isText ? (
            <Badge variant="brand">{t('delivery.assets.list.text')}</Badge>
          ) : (
            <Badge variant="off" className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" />
              {t('delivery.assets.list.binary')}
            </Badge>
          )}
        </MetaRow>
        <MetaRow label={t('delivery.assets.detail.fields.sha256')}>
          <span className="font-mono">{item.sha256}</span>
        </MetaRow>
        <MetaRow label={t('delivery.assets.detail.fields.mtime')}>
          {formatTime(new Date(item.mtimeMs).toISOString())}
        </MetaRow>
      </section>

      {/* 内容预览（内联，非模态） */}
      <section className="grid gap-2">
        <h4 className="text-[13px] font-semibold text-ink-1">{t('delivery.assets.detail.previewTitle')}</h4>

        {previewQuery.isLoading && <p className="text-xs text-ink-3">{t('delivery.assets.detail.previewLoading')}</p>}

        {sensitiveBlocked && (
          <div className="grid gap-2">
            <p className="rounded-lg border border-warn-bd bg-warn-bg px-3 py-2 text-sm text-warn">
              {t('delivery.assets.preview.sensitiveHint')}
            </p>
            <Button
              size="sm"
              className="w-fit"
              onClick={() => {
                setReasonTarget({ serverId: item.serverId, path: item.path })
              }}
            >
              {t('delivery.assets.detail.sensitiveOpen')}
            </Button>
          </div>
        )}

        {otherError !== null && <p className="text-sm text-destructive">{otherError}</p>}

        {result && (
          <div className="grid gap-2">
            <div className="flex flex-wrap items-center gap-2 text-xs text-ink-3">
              <span className="tnum">
                {t('delivery.assets.preview.meta', {
                  size: formatBytes(result.size),
                  sha256: shortHash(result.sha256),
                })}
              </span>
              {result.sensitive && (
                <Badge variant="crit" className="gap-1.5">
                  <span className="size-1.5 rounded-full bg-current" />
                  {t('delivery.assets.preview.sensitiveBadge')}
                </Badge>
              )}
              {result.truncated && (
                <Badge variant="warn" className="gap-1.5">
                  <span className="size-1.5 rounded-full bg-current" />
                  {t('delivery.assets.preview.truncated')}
                </Badge>
              )}
            </div>
            {result.binary ? (
              <p className="rounded-lg border border-border bg-surface-2 px-3 py-2 text-sm text-ink-3">
                {t('delivery.assets.preview.binaryOnly')}
              </p>
            ) : (
              <ScrollArea className="h-72 rounded-xl border border-border">
                <pre className="whitespace-pre-wrap p-3 font-mono text-xs text-ink-2">{result.content}</pre>
              </ScrollArea>
            )}
          </div>
        )}
      </section>

      {/* 敏感文件填原因（保留模态表单） */}
      <PreviewDialog
        target={reasonTarget}
        onOpenChange={(open) => {
          if (!open) {
            setReasonTarget(null)
          }
        }}
      />
    </div>
  )
}
