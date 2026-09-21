// 统一页面骨架占位：mock 建设中提示（页面标题已由页眉面包屑承担，内容区不再渲染大标题）。
// 各页面 agent 用真实内容替换所在页面文件时移除本组件的引用。
import { useTranslation } from 'react-i18next'

import { Badge } from '@beacon/ui'

export default function PageScaffold() {
  const { t } = useTranslation()
  return (
    <section className="grid gap-3">
      <div>
        <Badge variant="outline">{t('common.mockBuilding')}</Badge>
      </div>
    </section>
  )
}
