// 配置中心内部共享类型。

// 右侧编辑器打开的标签（一条配置一个标签，content 为编辑中的内容）
export interface OpenTab {
  configId: number
  dataId: string
  namespace: string
  group: string
  scopeLevel: string
  scopeTarget: string
  format: string
  content: string
}

// 编辑器视图模式：编辑 / Diff / 生效预览
export type ViewMode = 'edit' | 'diff' | 'effective'

// 左侧资源管理器树节点
export interface TreeNode {
  key: string
  label: string
  type: 'folder' | 'file'
  children?: TreeNode[]
  data?: Record<string, unknown>
}
