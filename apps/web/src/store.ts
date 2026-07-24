import { create } from 'zustand'

// Shell 全局状态：桌面侧栏折叠（FR-186）+ 小屏抽屉开关（FR-189）。
// 当前页高亮由路由（NavLink）自行判定。
interface ShellState {
  sidebarCollapsed: boolean
  toggleSidebar: () => void
  mobileNavOpen: boolean
  openMobileNav: () => void
  closeMobileNav: () => void
  toggleMobileNav: () => void
}

export const useShellStore = create<ShellState>((set) => ({
  sidebarCollapsed: false,
  toggleSidebar: () => {
    set((state) => ({ sidebarCollapsed: !state.sidebarCollapsed }))
  },
  mobileNavOpen: false,
  openMobileNav: () => {
    set({ mobileNavOpen: true })
  },
  closeMobileNav: () => {
    set({ mobileNavOpen: false })
  },
  toggleMobileNav: () => {
    set((state) => ({ mobileNavOpen: !state.mobileNavOpen }))
  },
}))
