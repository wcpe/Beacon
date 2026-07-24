// 查询字段包装：控件上方持续可见的小字段标签（输入后字段含义不随 placeholder 消失）。
// 无障碍名仍由控件自身 aria-label 提供，本标签为视觉呈现；连接明细 / 消息链路查询工具条共用。
import type { ReactNode } from 'react'

interface QueryFieldProps {
  // 字段标签（持续可见）
  label: ReactNode
  children: ReactNode
}

export default function QueryField({ label, children }: QueryFieldProps) {
  return (
    <div className="grid content-start gap-1">
      <span className="text-xs font-medium text-ink-4">{label}</span>
      {children}
    </div>
  )
}
