import { create } from 'zustand'

// Shell 全局状态：目前只管侧栏折叠；当前页高亮由路由（NavLink）自行判定
interface ShellState {
  sidebarCollapsed: boolean
  toggleSidebar: () => void
}

export const useShellStore = create<ShellState>((set) => ({
  sidebarCollapsed: false,
  toggleSidebar: () => {
    set((state) => ({ sidebarCollapsed: !state.sidebarCollapsed }))
  },
}))
