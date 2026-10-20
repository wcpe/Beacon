import type { ReactNode } from 'react'

interface PageSkeletonProps {
  children: ReactNode
  className?: string
}

export default function PageSkeleton({ children, className }: PageSkeletonProps) {
  return (
    <div data-slot="page-skeleton" aria-hidden="true" className={className}>
      {children}
    </div>
  )
}
