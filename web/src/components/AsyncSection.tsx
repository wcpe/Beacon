// 异步区块三态包装：统一列表/详情页的「加载中 / 加载失败 / 内容」渲染，消灭各页重复的状态分支。
// 空态由内容本身负责（如 DataTable 的空行），本组件只管 loading 与 error。

import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

interface AsyncSectionProps {
  // 是否加载中
  isLoading: boolean
  // 是否出错
  isError?: boolean
  // 错误对象（取 message 展示）
  error?: unknown
  // 加载中文案（默认取 i18n common.loading）；仅在未提供 skeleton 时使用
  loadingText?: ReactNode
  // 加载骨架（贴近真实内容形状）：提供时加载态渲染骨架而非纯文字，首屏不再空白/裸文字
  skeleton?: ReactNode
  // 加载成功后的内容
  children: ReactNode
}

export default function AsyncSection({
  isLoading,
  isError,
  error,
  loadingText,
  skeleton,
  children,
}: AsyncSectionProps) {
  const { t } = useTranslation()
  if (isLoading) {
    // 优先渲染骨架；无骨架时回退为纯文字提示（保持旧行为，向后兼容）
    if (skeleton) return <>{skeleton}</>
    return <p className="text-sm text-muted-foreground">{loadingText ?? t('common.loading')}</p>
  }
  if (isError) {
    const message = error instanceof Error ? error.message : String(error ?? t('common.unknownError'))
    return <p className="text-sm text-destructive">{t('common.loadFailed', { message })}</p>
  }
  return <>{children}</>
}
