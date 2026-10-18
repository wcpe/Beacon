// 布局 Shell：固定侧栏 + 固定页眉 + 独立滚动的内容区 + 路由出口。
// 页眉不随内容滚动；滚动只发生在内容区（overflow-y-auto），滚动条用 scrollbar-gutter:stable
// 预留占位——出现/消失都占位，避免布局横向跳动。
// 侧栏折叠触发器贴合悬浮卡片右缘，动画与卡片宽度保持同步。
// 内容区按宽度自适应缩放（见 useFitToWidth）：内容超出可视宽度时整体等比缩小到刚好放下，
// 因此 100% 浏览器缩放下只会有纵向滚动，绝不出现横向滚动条、也不会裁掉右侧内容。
// 订阅 mock 场景切换（FR-159），场景变化时全量失效查询让所有页面重取数据。
import { lazy, Suspense, useEffect, useLayoutEffect, useRef, type CSSProperties } from 'react'
import { useTranslation } from 'react-i18next'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'

import {
  subscribeMockScenario,
  subscribeMockDataVolume,
  subscribeMockLatency,
} from '@beacon/devmock/scenario'
import { useQueryClient } from '@tanstack/react-query'

import { isDemoMode } from '../demo-mode'
import LicensePage from '../pages/license'
import ApprovalsPage from '../pages/approvals'
import { ALL_PAGES } from '../routes'
import { useShellStore } from '../store'
import Header from './header'
import Sidebar, {
  SIDEBAR_GUTTER,
  SIDEBAR_MOTION,
  SIDEBAR_WIDTH_COLLAPSED,
  SIDEBAR_WIDTH_EXPANDED,
} from './sidebar'

/** 内容再窄也不缩到看不清，缩放下限 */
const MIN_FIT_ZOOM = 0.6

/** 缩放写入阈值：小于此差异不再写样式，保证迭代收敛、不自激死循环 */
const FIT_EPSILON = 0.005

const DemoMockDevTools =
  import.meta.env.DEV || import.meta.env.MODE === 'demo'
    ? lazy(() => import('../features/mock-dev-tools/mock-dev-tools'))
    : null

/**
 * 路由入场动画与滚动归顶用的「导航页键」。
 *
 * 直接拿 `location.pathname` 当 key 会有一个副作用：详情子路由（如 `/approvals/:requestId`）
 * 与查询式选中（如 `/changes?order=1`）都只是**同一页内的面板切换**，却会改变 pathname，
 * 导致整棵子树重挂载——表现为打开详情时页面「闪一下」、入场动画重播，同时列表筛选、
 * 查询结果与滚动位置全部丢失。
 *
 * 因此这里把 pathname 归一到它所属的导航页路径（取最长前缀匹配，保证 `/changes/history`
 * 不会被误归到 `/changes`）。只有真正切换页面才触发动画与归顶。
 */
function navPageKey(pathname: string): string {
  // 取最长前缀匹配：`/changes/history` 不能被 `/changes` 抢走。
  // 用 .at(0) 而非 [0]：`/license` 等不进 ALL_PAGES 的页面匹配不到，需安全退回完整 pathname。
  const matched = ALL_PAGES.filter(
    (page) => pathname === page.path || pathname.startsWith(`${page.path}/`),
  )
    .sort((a, b) => b.path.length - a.path.length)
    .at(0)
  return matched?.path ?? pathname
}

function readZoom(el: HTMLElement): number {
  const parsed = Number.parseFloat(el.style.getPropertyValue('zoom'))
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 1
}

/**
 * 让内容区在可视宽度内「整体缩小放下」，而非裁切右侧或撑出横向滚动条。
 *
 * 难点：内容常在异步取数后才变宽，而「复位 zoom → 量测 → 再写 zoom」会与 ResizeObserver
 * 自激成死循环。这里改用**比例迭代**：clientWidth 与 scrollWidth 同处元素自身坐标系，
 * 其比值不受当前 zoom 语义影响，故按 `next = cur * (client / scroll)` 逐步逼近；
 * 写入带阈值，差异收敛后不再写样式，迭代自然终止。
 * 路由切换与容器尺寸变化时先复位为 1，让内容有机会重新放大回原尺寸。
 */
function useFitToWidth(
  scrollRef: React.RefObject<HTMLElement | null>,
  contentRef: React.RefObject<HTMLElement | null>,
  routeKey: string,
): void {
  useLayoutEffect(() => {
    const scroll = scrollRef.current
    const content = contentRef.current
    if (!scroll || !content) {
      return
    }

    let timer: ReturnType<typeof setTimeout> | undefined
    // 有界迭代逼近：每次写 zoom 后同步读回布局，最多 5 轮即收敛
    const step = (): void => {
      for (let i = 0; i < 5; i += 1) {
        const client = content.clientWidth
        const scrollW = content.scrollWidth
        if (client <= 0 || scrollW <= client + 1) {
          break
        }
        const current = readZoom(content)
        const next = Math.max(MIN_FIT_ZOOM, current * (client / scrollW))
        if (Math.abs(next - current) <= FIT_EPSILON) {
          break
        }
        content.style.setProperty('zoom', String(next))
      }
    }
    // 用 setTimeout 而非 requestAnimationFrame：页面不绘制（后台标签 / 无头预览）时 rAF 不触发，
    // 会导致缩放永远算不出来。
    const schedule = (): void => {
      clearTimeout(timer)
      timer = setTimeout(step, 0)
    }
    const reset = (): void => {
      content.style.setProperty('zoom', '1')
      schedule()
    }

    content.style.setProperty('zoom', '1')
    step()
    // ResizeObserver 只报元素自身盒子尺寸，子元素溢出不会触发它——异步取数撑宽必须靠
    // MutationObserver 捕获 DOM 变化后重算（只观察子树结构，不观察属性，避免写 zoom 自激）。
    const contentObserver = new MutationObserver(schedule)
    contentObserver.observe(content, { childList: true, subtree: true })
    // 容器尺寸变化（窗口缩放 / 侧栏折叠）：先复位为 1 让内容有机会放大回原尺寸再收敛
    const scrollObserver = new ResizeObserver(reset)
    scrollObserver.observe(scroll)
    return () => {
      clearTimeout(timer)
      contentObserver.disconnect()
      scrollObserver.disconnect()
    }
  }, [scrollRef, contentRef, routeKey])
}

export default function AppShell() {
  const { t } = useTranslation()
  const sidebarCollapsed = useShellStore((state) => state.sidebarCollapsed)
  const mobileNavOpen = useShellStore((state) => state.mobileNavOpen)
  const closeMobileNav = useShellStore((state) => state.closeMobileNav)
  const queryClient = useQueryClient()
  const location = useLocation()
  const mainRef = useRef<HTMLElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)

  useFitToWidth(mainRef, contentRef, location.pathname)

  // 只在真正切换页面时归顶；打开详情子路由不应把列表滚回顶部
  const pageKey = navPageKey(location.pathname)
  useLayoutEffect(() => {
    if (mainRef.current) {
      mainRef.current.scrollTop = 0
    }
  }, [pageKey])

  useEffect(() => {
    const invalidateQueries = () => {
      void queryClient.invalidateQueries()
    }
    const unsubscribe = [
      subscribeMockScenario(invalidateQueries),
      subscribeMockDataVolume(invalidateQueries),
      subscribeMockLatency(invalidateQueries),
    ]
    return () => {
      for (const stop of unsubscribe) {
        stop()
      }
    }
  }, [queryClient])

  // 路由变化时关闭小屏抽屉，避免遮挡新页（FR-189）
  useEffect(() => {
    closeMobileNav()
  }, [location.pathname, closeMobileNav])

  const sidebarWidth = sidebarCollapsed ? SIDEBAR_WIDTH_COLLAPSED : SIDEBAR_WIDTH_EXPANDED
  // 主区让位 = 侧栏宽度 + 左右外沿（侧栏是居中的悬浮卡片，两侧都要留白）
  const shellInset = sidebarWidth + SIDEBAR_GUTTER * 2

  return (
    <div className="app-bg h-screen overflow-hidden text-foreground">
      <Sidebar />

      {/* 小屏遮罩：点按关闭抽屉 */}
      {mobileNavOpen ? (
        <button
          type="button"
          aria-label={t('common.sidebar.closeMobileOverlay')}
          className="fixed inset-0 z-30 bg-ink-1/40 md:hidden"
          onClick={closeMobileNav}
        />
      ) : null}


      <div
        className={[
          'flex h-screen flex-col transition-[padding-left]',
          SIDEBAR_MOTION,
          sidebarCollapsed ? 'delay-[50ms] motion-reduce:delay-0' : 'delay-0',
          'md:pl-[var(--shell-sidebar-width)]',
        ].join(' ')}
        style={{ '--shell-sidebar-width': `${String(shellInset)}px` } as CSSProperties}
      >
        <Header />
        {/* 内容区只纵向滚动；超宽内容由 useFitToWidth 等比缩小放下，不裁切、不横滚 */}
        <main
          ref={mainRef}
          className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto [scrollbar-gutter:stable]"
        >
          <div ref={contentRef} className="grid gap-5 p-4 sm:px-[22px] sm:py-[18px] md:flex md:h-full md:min-h-0 md:flex-col">
            {/* key 用导航页而非完整 pathname：详情子路由（/:id）与 ?order= 选中属于同页内面板切换，
                不应触发整棵子树重挂载与入场动画，否则打开详情会闪一下并丢失页面状态 */}
            <div key={pageKey} data-slot="route-transition" className="min-w-0 md:min-h-0 md:flex-1">
              <Routes>
                <Route element={<Navigate replace to="/dashboard" />} path="/" />
                {/* FR-190：开源许可页仅底栏入口，不进主导航 ALL_PAGES */}
                <Route element={<LicensePage />} path="/license" />
                {ALL_PAGES.map((page) => (
                  <Route element={<page.Component />} key={page.path} path={page.path} />
                ))}
                <Route element={<ApprovalsPage />} path="/approvals/:requestId" />
              </Routes>
            </div>
          </div>
        </main>
      </div>
      {isDemoMode() && DemoMockDevTools !== null ? (
        <Suspense fallback={null}>
          <DemoMockDevTools />
        </Suspense>
      ) : null}
    </div>
  )
}
