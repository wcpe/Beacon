// 文件清单详情（右侧非模态详情面板内容）：只展示元数据；正文经审批申请与一次性授权获取。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge, Button } from '@beacon/ui'
import type { AssetItem } from '@beacon/contracts'

import PreviewDialog from './preview-dialog'
import { formatBytes, formatTime } from './format'

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
  const [previewTarget, setPreviewTarget] = useState<{ serverId: string; path: string } | null>(null)

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

      {/* 文件正文由审批后的 Agent 命令与一次性授权提供，详情面板只保留元数据。 */}
      <section className="grid gap-2">
        <h4 className="text-[13px] font-semibold text-ink-1">{t('delivery.assets.detail.previewTitle')}</h4>
        <p className="text-sm text-ink-3">{t('delivery.assets.detail.approvalHint')}</p>
        <Button size="sm" className="w-fit" onClick={() => { setPreviewTarget({ serverId: item.serverId, path: item.path }) }}>
          {t('delivery.assets.detail.requestPreview')}
        </Button>
      </section>

      {/* 专用审批表单不调用旧预览接口。 */}
      <PreviewDialog
        target={previewTarget}
        onOpenChange={(open) => {
          if (!open) {
            setPreviewTarget(null)
          }
        }}
      />
    </div>
  )
}
