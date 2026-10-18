// 配置文件空态引导：无任何配置文件时代替空表格，用一句话讲清「配置在这里存版本」，
// 大号按钮直接打开本页已有的新建弹窗，给新手一个明确的下一步。
import { useTranslation } from 'react-i18next'

import { FilePlus2 } from 'lucide-react'

import { Button } from '@beacon/ui'

interface EmptyGuideProps {
  // 打开新建配置文件弹窗（configs/create-dialog）
  onCreate: () => void
}

export default function EmptyGuide({ onCreate }: EmptyGuideProps) {
  const { t } = useTranslation()
  return (
    <div className="grid justify-items-center gap-4 px-4 py-10 text-center">
      <div className="grid justify-items-center gap-1">
        <h3 className="text-sm font-semibold text-ink-1">{t('delivery.guides.configs.title')}</h3>
        <p className="max-w-xl text-sm text-muted-foreground">{t('delivery.guides.configs.desc')}</p>
      </div>
      <Button size="lg" variant="outline" onClick={onCreate}>
        <FilePlus2 className="size-4" aria-hidden />
        {t('delivery.guides.configs.cta')}
      </Button>
    </div>
  )
}
