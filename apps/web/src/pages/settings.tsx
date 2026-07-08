// 运维设置页（/settings）：Legacy 热改运行参数（分组编辑保存）+ 健康因子权重（编辑 + rev 历史）+ 归档与清理。
// 页内用锚点 rail 组织三大块，侧栏保持扁平。
import { useTranslation } from 'react-i18next'
import { SlidersHorizontal } from 'lucide-react'

import { AnchorRailLayout, AnchorSectionBlock, SectionHeader, type AnchorSection } from '@beacon/ui'

import ArchiveBlock from './settings/archive-block'
import SettingsBlock from './settings/settings-block'
import WeightsBlock from './settings/weights-block'

export default function SettingsPage() {
  const { t } = useTranslation()

  const sections: AnchorSection[] = [
    { id: 'settings-params', label: t('system.settings.paramsTitle') },
    { id: 'settings-weights', label: t('system.settings.weightsSection') },
    { id: 'settings-archive', label: t('system.settings.archiveSection') },
  ]

  return (
    <section className="grid gap-4">
      <SectionHeader size="lg" icon={<SlidersHorizontal className="size-5" />} title={t('nav.settings')} />
      <AnchorRailLayout sections={sections} ariaLabel={t('nav.settings')}>
        <AnchorSectionBlock id="settings-params" title={t('system.settings.paramsTitle')}>
          <SettingsBlock />
        </AnchorSectionBlock>
        <AnchorSectionBlock id="settings-weights" title={t('system.settings.weightsSection')}>
          <WeightsBlock />
        </AnchorSectionBlock>
        <AnchorSectionBlock id="settings-archive" title={t('system.settings.archiveSection')}>
          <ArchiveBlock />
        </AnchorSectionBlock>
      </AnchorRailLayout>
    </section>
  )
}
