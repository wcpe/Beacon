// 配置文件空态引导：无任何配置文件时代替空表格，用一句话讲清「配置在这里存版本」，
// 大号按钮直接打开本页已有的新建弹窗，给新手一个明确的下一步。
import { useTranslation } from 'react-i18next'

import { FilePlus2 } from 'lucide-react'

import { Button } from '@beacon/ui'

import EmptyGuideFrame from '../../features/shared/empty-guide-frame'

interface EmptyGuideProps {
  // 打开新建配置文件弹窗（configs/create-dialog）
  onCreate: () => void
}

export default function EmptyGuide({ onCreate }: EmptyGuideProps) {
  const { t } = useTranslation()
  return (
    <EmptyGuideFrame title={t('delivery.guides.configs.title')} description={t('delivery.guides.configs.desc')}>
      <Button size="lg" variant="outline" onClick={onCreate}>
        <FilePlus2 className="size-4" aria-hidden />
        {t('delivery.guides.configs.cta')}
      </Button>
    </EmptyGuideFrame>
  )
}
