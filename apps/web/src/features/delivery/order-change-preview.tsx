// 变更内容预览装配（共享控件）：包住纯展示 ChangePreview——把 config_change 项的
// 版本 id 反查配置中心（目标版本 → 所属文件名 / 版本号，来源版本 → 版本号），
// 并接好行级 diff 懒加载（ConfigVersionDiff）。反查失败时回退原始作用域 + 版本 id 展示，
// 不阻塞文件差异清单。/changes 详情「变更项」Tab 与历史详情「变更内容」共用。
import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import type { ChangeOrderItem } from '../../api/delivery-changes'
import { fetchConfigFileDetail, fetchConfigVersion } from '../../api/delivery-configs'
import ChangePreview, { type ConfigChangeLabel } from './change-preview'
import ConfigVersionDiff from './config-version-diff'

interface OrderChangePreviewProps {
  items: ChangeOrderItem[]
}

export default function OrderChangePreview({ items }: OrderChangePreviewProps) {
  const { t } = useTranslation()

  const configItems = items.filter((item) => item.kind === 'config_change' && item.configToVersionId !== null)

  // 逐项反查目标 / 来源版本与所属文件（数量为单内配置项数，通常个位数，并行取回）
  const labelQuery = useQuery({
    queryKey: [
      'change-orders',
      'config-change-labels',
      configItems.map((item) => `${String(item.configFromVersionId)}>${String(item.configToVersionId)}`).join(','),
    ],
    enabled: configItems.length > 0,
    queryFn: async () => {
      const entries = await Promise.all(
        configItems.map(async (item) => {
          // enabled 过滤保证 configToVersionId 非空，此处兜底跳过
          if (item.configToVersionId === null) {
            return null
          }
          const to = await fetchConfigVersion(item.configToVersionId)
          const [file, from] = await Promise.all([
            fetchConfigFileDetail(to.configFileId),
            item.configFromVersionId === null ? Promise.resolve(null) : fetchConfigVersion(item.configFromVersionId),
          ])
          const label: ConfigChangeLabel = {
            fileName: file.name,
            fromVersionNo: from?.versionNo ?? null,
            toVersionNo: to.versionNo,
          }
          return [item.configToVersionId, label] as const
        }),
      )
      return new Map(entries.filter((entry) => entry !== null))
    },
  })

  // 反查失败 / 未返回时回 null → ChangePreview 回退原始展示
  const configLabelOf = (item: ChangeOrderItem): ConfigChangeLabel | null => {
    if (item.configToVersionId === null) {
      return null
    }
    return labelQuery.data?.get(item.configToVersionId) ?? null
  }

  const renderConfigDiff = (item: ChangeOrderItem): ReactNode => {
    if (item.configToVersionId === null) {
      return null
    }
    const label = configLabelOf(item)
    return (
      <ConfigVersionDiff
        fromVersionId={item.configFromVersionId}
        toVersionId={item.configToVersionId}
        fromLabel={
          label?.fromVersionNo == null
            ? t('delivery.preview.versionDiff.fromEmpty')
            : t('delivery.preview.versionDiff.fromLabel', { no: label.fromVersionNo })
        }
        toLabel={t('delivery.preview.versionDiff.toLabel', { no: label === null ? '-' : label.toVersionNo })}
      />
    )
  }

  return <ChangePreview items={items} configLabelOf={configLabelOf} renderConfigDiff={renderConfigDiff} />
}
