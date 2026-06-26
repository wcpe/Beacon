// 锚点 rail 布局（FR-108）：左侧 sticky 分区锚点导航 + 右侧滚动内容 + scroll-spy 高亮当前分区。
// 治「重内容页」——页内靠锚点组织各分区，侧栏保持扁平、不加全局三级路由。
// 供 控制面健康 / 运维设置 / 版本与更新 复用；纯展示，不持有业务状态。
//
// 用法：
//   <AnchorRailLayout sections={[{ id, label, dot }]} ariaLabel="...">
//     <AnchorSection id="...">...</AnchorSection>
//     ...
//   </AnchorRailLayout>
// 各分区由调用方用 AnchorSection（或自带 id 的容器）渲染，id 须与 sections[].id 一一对应。

import { useEffect, useRef, useState, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

// 单个锚点分区描述：id（与内容分区 DOM id 对应）+ 显示标签 + 可选状态色点。
export interface AnchorSection {
  id: string
  label: string
  // 可选状态色点（如健康色圆点）：渲染在 rail 项标签前。
  dot?: ReactNode
}

interface AnchorRailLayoutProps {
  sections: AnchorSection[]
  children: ReactNode
  // rail 导航的无障碍名称
  ariaLabel: string
  className?: string
}

export default function AnchorRailLayout({ sections, children, ariaLabel, className }: AnchorRailLayoutProps) {
  // 当前高亮分区 id（scroll-spy 命中或点击锚点设定）；默认首个分区。
  const [activeId, setActiveId] = useState<string>(sections[0]?.id ?? '')
  // 右侧滚动内容容器引用（作为 IntersectionObserver 的 root 与平滑滚动目标）。
  const contentRef = useRef<HTMLDivElement>(null)

  // scroll-spy：观察各分区根，命中视口顶部区域者高亮。jsdom 无真实布局，IO 缺失时安全跳过（仅锚点可点）。
  useEffect(() => {
    const root = contentRef.current
    if (!root || typeof IntersectionObserver === 'undefined') return
    const observer = new IntersectionObserver(
      (entries) => {
        // 取最先进入顶部区域的可见分区作为当前项。
        const visible = entries
          .filter((e) => e.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)
        if (visible.length > 0) {
          const id = visible[0].target.getAttribute('id')
          if (id) setActiveId(id)
        }
      },
      // 顶部 20% 处作为命中线：分区滚到接近顶部即视为当前。
      { root, rootMargin: '0px 0px -70% 0px', threshold: 0 },
    )
    for (const s of sections) {
      const el = root.querySelector(`#${CSS.escape(s.id)}`)
      if (el) observer.observe(el)
    }
    return () => observer.disconnect()
  }, [sections])

  // 点击 rail 项：高亮并平滑滚动定位到对应分区。
  const onJump = (id: string) => {
    setActiveId(id)
    const root = contentRef.current
    const el = root?.querySelector(`#${CSS.escape(id)}`)
    if (el && 'scrollIntoView' in el) {
      ;(el as HTMLElement).scrollIntoView({ behavior: 'smooth', block: 'start' })
    }
  }

  return (
    <div className={cn('flex min-h-0 flex-1 gap-6', className)}>
      {/* 左 sticky rail：分区锚点列表 */}
      <nav aria-label={ariaLabel} className="sticky top-0 hidden w-44 shrink-0 self-start py-1 md:block">
        <ul className="space-y-0.5">
          {sections.map((s) => {
            const active = s.id === activeId
            return (
              <li key={s.id}>
                <button
                  type="button"
                  aria-current={active ? 'true' : undefined}
                  onClick={() => onJump(s.id)}
                  className={cn(
                    'flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm transition-colors',
                    active
                      ? 'bg-muted font-medium text-foreground'
                      : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground',
                  )}
                >
                  {s.dot && <span className="shrink-0">{s.dot}</span>}
                  <span className="truncate">{s.label}</span>
                </button>
              </li>
            )
          })}
        </ul>
      </nav>

      {/* 右滚动内容：各分区按 id 锚定 */}
      <div ref={contentRef} className="min-w-0 flex-1 overflow-y-auto scrollbar-hide">
        {children}
      </div>
    </div>
  )
}

// 锚点分区容器：带 id（scroll-spy 与平滑滚动目标）+ 分区标题（标题 + 细线）+ 内容。
// 标题用「区段标题 + 底部细线」轻分隔，替代 Card 外壳（卡片降级）。
export function AnchorSectionBlock({
  id,
  title,
  children,
  className,
}: {
  id: string
  title: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section id={id} className={cn('scroll-mt-2 pb-6', className)}>
      <h2 className="mb-2 border-b pb-1.5 text-base font-medium">{title}</h2>
      {children}
    </section>
  )
}
