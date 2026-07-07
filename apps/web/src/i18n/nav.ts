// 导航域文案：侧栏分组标题与页面标题（页面标题同时用于页面骨架的 heading）。
// 真源对照 docs/UX.md §2；新增页面先改 UX.md 再回填本文件。
export const nav = {
  groups: {
    cluster: '集群',
    observability: '可观测',
    delivery: '交付',
    system: '系统',
  },
  dashboard: '运维总览',
  servers: '服务器',
  zones: '区服分配',
  topology: '拓扑',
  serviceAnalysis: '服务分析',
  commands: '命令观测',
  audits: '审计',
  alertEvents: '告警事件',
  assets: '文件资产',
  configs: '配置中心',
  changes: '变更单',
  changesHistory: '交付历史',
  settings: '运维设置',
  system: '控制面健康',
  systemVersion: '版本与更新',
  apiKeys: '密钥',
  namespaces: 'namespace',
} as const
