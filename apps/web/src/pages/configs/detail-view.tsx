// 配置文件详情视图：顶部返回列表 + 文件名 / 格式 / 描述，四个 Tab（作用域概览 / 有效配置 / 版本链 / 差异对比）。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Button,
  SectionHeader,
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

interface DetailViewProps {
  fileId: number
  onBack: () => void
}

export default function DetailView({ fileId, onBack }: DetailViewProps) {
  const { t } = useTranslation()
  const [tab, setTab] = useState('scopes')

  const detailQuery = useQuery({
    queryKey: ['configs', 'detail', fileId],
    queryFn: () => fetchConfigFileDetail(fileId),
  })

  return (
    <section className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={onBack}>
            {t('delivery.configs.detail.backToList')}
          </Button>
          {detailQuery.data && (
            <SectionHeader
              title={<span className="font-mono">{detailQuery.data.name}</span>}
              count={detailQuery.data.description || undefined}
            />
          )}
        </div>
        {detailQuery.data && <Badge variant="outline">{detailQuery.data.format}</Badge>}
      </div>

      <AsyncSection
        isLoading={detailQuery.isLoading}
        isError={detailQuery.isError}
        error={detailQuery.error}
      >
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="scopes">{t('delivery.configs.detail.tabs.scopes')}</TabsTrigger>
            <TabsTrigger value="effective">{t('delivery.configs.detail.tabs.effective')}</TabsTrigger>
            <TabsTrigger value="versions">{t('delivery.configs.detail.tabs.versions')}</TabsTrigger>
            <TabsTrigger value="diff">{t('delivery.configs.detail.tabs.diff')}</TabsTrigger>
          </TabsList>
          <TabsContent value="scopes">
            <ScopesTab fileId={fileId} />
          </TabsContent>
          <TabsContent value="effective">
            <EffectiveTab fileId={fileId} />
          </TabsContent>
          <TabsContent value="versions">
            <VersionsTab fileId={fileId} />
          </TabsContent>
          <TabsContent value="diff">
            <DiffTab fileId={fileId} />
          </TabsContent>
        </Tabs>
      </AsyncSection>
    </section>
  )
}
