// 配置版本行级 diff：按版本 id 拉取 from / to 两个版本内容（from 为空 = 新增配置），
// 复用 TextDiff 双栏渲染。向导选配置步预览与第五步变更内容预览共用。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection } from '@beacon/ui'

import { fetchConfigVersion } from '../../api/delivery-configs'
import TextDiff from './text-diff'

interface ConfigVersionDiffProps {
  fromVersionId: number | null
  toVersionId: number
  fromLabel: string
  toLabel: string
}

export default function ConfigVersionDiff({
  fromVersionId,
  toVersionId,
  fromLabel,
  toLabel,
}: ConfigVersionDiffProps) {
  const { t } = useTranslation()

  // 两个版本内容并行取回；from 缺省（首个版本）时左栏为空串
  const query = useQuery({
    queryKey: ['config-version-diff', fromVersionId, toVersionId],
    queryFn: async () => {
      const [from, to] = await Promise.all([
        fromVersionId === null ? Promise.resolve(null) : fetchConfigVersion(fromVersionId),
        fetchConfigVersion(toVersionId),
      ])
      return { fromContent: from?.content ?? '', toContent: to.content }
    },
  })

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      loadingText={t('delivery.preview.versionDiff.loading')}
    >
      {query.data && (
        <TextDiff
          left={query.data.fromContent}
          right={query.data.toContent}
          leftLabel={fromLabel}
          rightLabel={toLabel}
        />
      )}
    </AsyncSection>
  )
}
