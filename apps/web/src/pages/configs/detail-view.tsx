// 配置文件详情视图：顶部返回列表 + 文件名 / 格式 / 描述，四个 Tab（作用域概览 / 有效配置 / 版本链 / 差异对比）。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, FileCog } from 'lucide-react'

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
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="outline" size="sm" onClick={onBack}>
          <ArrowLeft className="size-3.5" aria-hidden />
          {t('delivery.configs.detail.backToList')}
        </Button>
        {detailQuery.data && (
          <SectionHeader
            className="flex-1"
            icon={<FileCog className="size-4" />}
            title={<span className="font-mono text-ink-1">{detailQuery.data.name}</span>}
            count={detailQuery.data.description || undefined}
            actions={<Badge variant="brand">{detailQuery.data.format}</Badge>}
          />
        )}
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
          <TabsContent value="scopes" className="pt-4">
            <ScopesTab fileId={fileId} />
          </TabsContent>
          <TabsContent value="effective" className="pt-4">
            <EffectiveTab fileId={fileId} />
          </TabsContent>
          <TabsContent value="versions" className="pt-4">
            <VersionsTab fileId={fileId} />
          </TabsContent>
          <TabsContent value="diff" className="pt-4">
            <DiffTab fileId={fileId} />
          </TabsContent>
        </Tabs>
      </AsyncSection>
    </section>
  )
}
