// 文件资产页（/assets，只读）：看目录清单、哈希、内容预览与 diff。
// 页头统一为「图标 + 大标题 + 一行解释 + 右侧常用操作」（PageHeader），本页的命名空间范围
// 取自页眉观测范围选择器，页内不再重复放命名空间选择器。四类视图独立切换（清单主从 /
// 扫描概要 / 跨服比对 / 两侧差异），避免多块面板堆叠导致的超长页面。
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { FolderArchive, ShieldAlert } from 'lucide-react'

import { Button, PageHeader, TableSkeleton, Tabs, TabsContent, TabsList, TabsTrigger } from '@beacon/ui'

import DocHelp from '../features/delivery/doc-help'
import { fetchScanStatus } from '../api/delivery-assets'
import { useDefaultObservationNamespaceState } from '../features/env/use-default-namespace'
import ScanPanel from './assets/scan-panel'
import ManifestPanel from './assets/manifest-panel'
import ComparePanel from './assets/compare-panel'
import DiffPanel from './assets/diff-panel'
import EmptyGuide from './assets/empty-guide'
import SensitiveRulesDialog from './assets/sensitive-rules-dialog'

export default function AssetsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [view, setView] = useState('manifest')
  const [rulesOpen, setRulesOpen] = useState(false)

  // 命名空间范围唯一真源是页眉的观测范围选择器（页内不再有自己的选择器）。
  // 进页面时若页眉还停在「全部命名空间」，由 useDefaultObservationNamespaceState 自动补第一个
  // 可用命名空间并写回页眉范围（页眉与页面因此始终一致）；用户已选定的范围不会被覆盖。
  // namespaceId 仍为 0 只有两种情况：范围还在解析（pending → 骨架），或确实没有可用命名空间（走引导）。
  // 各面板的查询均为 `enabled: namespaceId > 0`，为 0 时不发请求。
  const { namespaceId, pending: scopePending } = useDefaultObservationNamespaceState()

  // 清单独有探测：扫描概要每服一行，total 为 0 说明该命名空间还没有任何 agent 清单上报。
  // 只取 1 行，成本可忽略；清单面板本身不改，空态引导统一在容器层判定与渲染。
  const probeQuery = useQuery({
    queryKey: ['assets', 'manifest-probe', namespaceId],
    queryFn: () => fetchScanStatus(namespaceId, undefined, 1, 1),
    enabled: namespaceId > 0,
  })
  const noManifest =
    namespaceId > 0 &&
    !probeQuery.isLoading &&
    !probeQuery.isError &&
    (probeQuery.data?.total ?? 0) === 0

  // 首次上报后无需刷新整页：让清单相关查询一起重取
  const refreshManifest = (): void => {
    void queryClient.invalidateQueries({ queryKey: ['assets'] })
  }

  return (
    <section className="grid gap-4 md:flex md:h-full md:min-h-0 md:flex-col">
      <SensitiveRulesDialog open={rulesOpen} onOpenChange={setRulesOpen} />

      {/* 页头：左=图标 + 大标题 + 一句话说明本页做什么；右=常用操作 + 文档入口 */}
      <PageHeader
        icon={<FolderArchive />}
        title={t('nav.assets')}
        description={t('delivery.assets.pageDesc')}
        actions={
          <>
            {/* 文件差异的下发统一走变更单：本页只看不改，去向入口留在页头 */}
            <Button asChild variant="outline" size="sm">
              <Link to="/changes">{t('delivery.assets.goChanges')}</Link>
            </Button>
            {/* 敏感路径规则编辑（FR-164）：命中 glob 的文件预览 / diff 需原因放行 */}
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setRulesOpen(true)
              }}
            >
              <ShieldAlert className="size-3.5" aria-hidden />
              {t('delivery.assets.sensitiveRules.manage')}
            </Button>
            {/* 页头文档入口：本页功能解释由 description 承担，这里只负责跳使用者文档 */}
            <DocHelp topic="assets" />
          </>
        }
      />

      <Tabs value={view} onValueChange={setView} className="md:min-h-0 md:flex-1 md:flex-col">
        <TabsList variant="line">
          <TabsTrigger value="manifest">{t('delivery.assets.list.title')}</TabsTrigger>
          <TabsTrigger value="scan">{t('delivery.assets.scan.title')}</TabsTrigger>
          <TabsTrigger value="compare">{t('delivery.assets.compare.title')}</TabsTrigger>
          <TabsTrigger value="diff">{t('delivery.assets.diff.title')}</TabsTrigger>
        </TabsList>
        {namespaceId > 0 ? (
          <>
            <TabsContent value="manifest" className="pt-4 md:min-h-0 md:flex-1 md:overflow-y-auto md:data-[state=active]:flex md:data-[state=active]:flex-col">
              {noManifest ? (
                <EmptyGuide onRefresh={refreshManifest} />
              ) : (
                <div className="grid gap-3 md:min-h-0 md:flex-1 md:flex md:flex-col">
                  {/* 白话提示：清单不是实时扫描结果，先讲清数据来源与「对不上怎么办」 */}
                  <p className="text-xs text-ink-3">{t('delivery.tips.assetsListSource')}</p>
                  <ManifestPanel namespaceId={namespaceId} />
                </div>
              )}
            </TabsContent>
            <TabsContent value="scan" className="pt-4 md:min-h-0 md:flex-1 md:overflow-y-auto md:data-[state=active]:flex md:data-[state=active]:flex-col">
              {/* 扫描概要与清单同源（每服一行清单摘要），零数据时给同一份引导，避免两处口径不一致 */}
              {noManifest ? <EmptyGuide onRefresh={refreshManifest} /> : <ScanPanel namespaceId={namespaceId} />}
            </TabsContent>
            <TabsContent value="compare" className="pt-4 md:min-h-0 md:flex-1 md:overflow-y-auto md:data-[state=active]:flex md:data-[state=active]:flex-col">
              <ComparePanel namespaceId={namespaceId} />
            </TabsContent>
            <TabsContent value="diff" className="pt-4 md:min-h-0 md:flex-1 md:overflow-y-auto md:data-[state=active]:flex md:data-[state=active]:flex-col">
              <DiffPanel namespaceId={namespaceId} />
            </TabsContent>
          </>
        ) : scopePending ? (
          // 范围解析中（env 选项加载 / 默认值回流）：给骨架，不要闪出「请先选命名空间」的引导
          <div className="pt-4 md:min-h-0 md:flex-1 md:overflow-y-auto" data-slot="assets-scope-pending">
            <TableSkeleton columns={4} rows={6} />
          </div>
        ) : (
          // 确实没有可用命名空间（或页眉范围失效）：不渲染面板（面板按 namespaceId 取数，0 会取不到
          // 任何数据），也不伪造数据，直接指向页眉的选择器
          <p data-slot="assets-scope-required" className="pt-4 text-sm text-ink-3">
            {t('delivery.assets.scopeRequired')}
          </p>
        )}
      </Tabs>
    </section>
  )
}
