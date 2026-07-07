// 系统域文案（/settings /system /system/version /api-keys /namespaces）：由系统域页面 agent 维护，其他域勿改
export const system = {
  settings: {
    mission: '采样、保留期、健康权重等运行参数；含归档与清理',
  },
  health: {
    mission: 'Beacon 自身运行时与子系统健康',
  },
  version: {
    mission: '版本、渠道与在线更新',
  },
  apiKeys: {
    mission: 'API 密钥管理',
  },
  namespaces: {
    mission: 'namespace 管理与互通信任关系',
  },
} as const
