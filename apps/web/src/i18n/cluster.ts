// 集群域文案（/servers /zones /topology）：由集群域页面 agent 维护，其他域勿改
export const cluster = {
  servers: {
    mission: '服务器资产：注册待确认、身份绑定、禁用 / 解绑 / 换区、健康详情',
  },
  zones: {
    mission: 'BC / 大区 / 小区结构与子服分配',
  },
  topology: {
    mission: 'BC-子服链路、消息流、请求拓扑与异常链路',
  },
} as const
