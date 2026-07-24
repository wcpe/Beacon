// 配置文件详情（右侧非模态详情面板内容）：文件名 / 格式 / 描述 + 元数据编辑入口 +
// 四个 Tab（作用域概览 / 有效配置 / 版本链 / 差异对比）。
// 面板的关闭由外层 MasterDetail 承担，此处不渲染返回按钮。
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Pencil, ShieldAlert } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from '@beacon/ui'

import { fetchConfigFileDetail } from '../../api/delivery-configs'
import ScopesTab from './scopes-tab'
import EffectiveTab from './effective-tab'
import VersionsTab from './versions-tab'
import DiffTab from './diff-tab'
import MetadataDialog from './metadata-dialog'

interface DetailViewProps {
  fileId: number
}

export default function DetailView({ fileId }: DetailViewProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState('scopes')
  const [metadataOpen, setMetadataOpen] = useState(false)

  const detailQuery = useQuery({
    queryKey: ['configs', 'detail', fileId],
    queryFn: () => fetchConfigFileDetail(fileId),
  })

  return (
    <div className="grid gap-3">
      {detailQuery.data && (
        <div className="grid gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm text-ink-1">{detailQuery.data.name}</span>
            <Badge variant="brand">{detailQuery.data.format}</Badge>
            {detailQuery.data.sensitivePaths.length > 0 && (
              <Badge variant="warn" className="gap-1">
                <ShieldAlert className="size-3" aria-hidden />
                {t('delivery.configs.detail.metadata.sensitiveCount', {
                  count: detailQuery.data.sensitivePaths.length,
                })}
              </Badge>
            )}
            <Button
              size="sm"
              variant="ghost"
              className="ml-auto"
              onClick={() => {
                setMetadataOpen(true)
              }}
            >
              <Pencil className="size-3.5" aria-hidden />
              {t('delivery.configs.detail.metadata.edit')}
            </Button>
          </div>
          {detailQuery.data.description !== '' && (
            <p className="text-xs text-ink-3">{detailQuery.data.description}</p>
          )}
        </div>
      )}

      {metadataOpen && detailQuery.data && (
        <MetadataDialog
          file={detailQuery.data}
          onOpenChange={(open) => {
            if (!open) {
              setMetadataOpen(false)
            }
          }}
          onSaved={() => {
            setMetadataOpen(false)
            void queryClient.invalidateQueries({ queryKey: ['configs'] })
          }}
        />
      )}

      <AsyncSection
        isLoading={detailQuery.isLoading}
        isError={detailQuery.isError}
        error={detailQuery.error}
      >
        {detailQuery.data && (
          <Tabs value={tab} onValueChange={setTab}>
            <TabsList>
              <TabsTrigger value="scopes">{t('delivery.configs.detail.tabs.scopes')}</TabsTrigger>
              <TabsTrigger value="effective">{t('delivery.configs.detail.tabs.effective')}</TabsTrigger>
              <TabsTrigger value="versions">{t('delivery.configs.detail.tabs.versions')}</TabsTrigger>
              <TabsTrigger value="diff">{t('delivery.configs.detail.tabs.diff')}</TabsTrigger>
            </TabsList>
            <TabsContent value="scopes" className="pt-4">
              <ScopesTab fileId={fileId} file={detailQuery.data} />
            </TabsContent>
            <TabsContent value="effective" className="pt-4">
              <EffectiveTab fileId={fileId} file={detailQuery.data} />
            </TabsContent>
            <TabsContent value="versions" className="pt-4">
              <VersionsTab fileId={fileId} />
            </TabsContent>
            <TabsContent value="diff" className="pt-4">
              <DiffTab fileId={fileId} file={detailQuery.data} />
            </TabsContent>
          </Tabs>
        )}
      </AsyncSection>
    </div>
  )
}
