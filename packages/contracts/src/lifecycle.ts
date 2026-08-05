// 生命周期管理面响应契约（FR-215 至 FR-218）。

/** 资源自身的生命周期；有效运行资格还会叠加父级 namespace 状态。 */
export type LifecycleStatus = 'active' | 'archived' | 'tombstoned'

/** 生命周期审批操作的页面动作名。 */
export type LifecycleAction = 'archive' | 'restore' | 'permanent-delete'

/** 墓碑只读摘要；不含可恢复或敏感运行数据。 */
export interface TombstoneSummary {
  tombstonedAt: string | null
  tombstonedBy: string | null
  tombstoneReason: string | null
  approvalRequestId: string | null
}

/** 生命周期影响预览的公共基础字段。 */
export interface LifecycleImpact {
  action: LifecycleAction
  currentLifecycle: LifecycleStatus
  targetLifecycle: LifecycleStatus
  effectiveActive: boolean
}
