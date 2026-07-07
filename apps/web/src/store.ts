import { create } from 'zustand'

export type AppView = 'overview' | 'topology' | 'settings'

interface ShellState {
  activeView: AppView
  sidebarCollapsed: boolean
  setActiveView: (view: AppView) => void
  toggleSidebar: () => void
}

export const useShellStore = create<ShellState>((set) => ({
  activeView: 'overview',
  sidebarCollapsed: false,
  setActiveView: (activeView) => { set({ activeView }); },
  toggleSidebar: () => { set((state) => ({ sidebarCollapsed: !state.sidebarCollapsed })); },
}))
