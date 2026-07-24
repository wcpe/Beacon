// 统一页面骨架占位：页面标题 + 唯一职责（UX.md §2）+ mock 建设中提示。
// 各页面 agent 用真实内容替换所在页面文件时移除本组件的引用。
import { useTranslation } from 'react-i18next'

import { Badge, PageHeader } from '@beacon/ui'

interface PageScaffoldProps {
  // 页面标题的 i18n 键（nav 域，与侧栏共用）
  titleKey: string
  // 页面唯一职责的 i18n 键（所属页面域）
  missionKey: string
}

export default function PageScaffold({ titleKey, missionKey }: PageScaffoldProps) {
  const { t } = useTranslation()
  return (
    <section className="grid gap-3">
      <PageHeader title={t(titleKey)} description={t(missionKey)} />
      <div>
        <Badge variant="outline">{t('common.mockBuilding')}</Badge>
      </div>
    </section>
  )
}
