// 异步区块三态包装：统一列表/详情页的「加载中 / 加载失败 / 内容」渲染，消灭各页重复的状态分支。
// 空态由内容本身负责（如 DataTable 的空行），本组件只管 loading 与 error。

import type { ReactNode } from 'react'

interface AsyncSectionProps {
  // 是否加载中
  isLoading: boolean
  // 是否出错
  isError?: boolean
  // 错误对象（取 message 展示）
  error?: unknown
  // 加载中文案（默认「加载中…」）
  loadingText?: ReactNode
  // 加载成功后的内容
  children: ReactNode
}

export default function AsyncSection({
  isLoading,
  isError,
  error,
  loadingText = '加载中…',
  children,
}: AsyncSectionProps) {
  if (isLoading) {
    return <p className="text-sm text-muted-foreground">{loadingText}</p>
  }
  if (isError) {
    const message = error instanceof Error ? error.message : String(error ?? '未知错误')
    return <p className="text-sm text-destructive">加载失败：{message}</p>
  }
  return <>{children}</>
}
