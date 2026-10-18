import type { ReactNode } from 'react'

interface EmptyGuideFrameProps {
  title: string
  description: string
  children: ReactNode
}

export default function EmptyGuideFrame({ title, description, children }: EmptyGuideFrameProps) {
  return (
    <div className="grid justify-items-center gap-4 px-4 py-10 text-center">
      <div className="grid justify-items-center gap-1">
        <h3 className="text-sm font-semibold text-ink-1">{title}</h3>
        <p className="max-w-xl text-sm text-muted-foreground">{description}</p>
      </div>
      {children}
    </div>
  )
}
