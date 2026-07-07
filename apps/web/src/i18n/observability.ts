// 可观测域文案（/service-analysis /commands /audits /alert-events）：由可观测域页面 agent 维护，其他域勿改
export const observability = {
  serviceAnalysis: {
    mission: '指标聚合、趋势与对比',
  },
  commands: {
    mission: 'agent 命令双向生命周期与队列',
  },
  audits: {
    mission: '审计查询、追溯与导出',
  },
  alertEvents: {
    mission: '告警事件列表与处理状态',
  },
} as const
