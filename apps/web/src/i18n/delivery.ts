// 交付域文案（/assets /configs /changes /changes/history，FR-170）：由交付域页面 agent 维护，其他域勿改
export const delivery = {
  assets: {
    mission: '看：目录清单、哈希、内容预览与 diff，只读',
  },
  configs: {
    mission: '改：作用域配置编辑、校验、版本管理（下发走变更单）',
  },
  changes: {
    mission: '发：变更单创建、影响预览、审批、灰度批次、生效观察',
  },
  changesHistory: {
    mission: '溯：任务 / 批次 / 单服状态与整单回滚',
  },
} as const
