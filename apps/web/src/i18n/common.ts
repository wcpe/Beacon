// 公共域文案：Shell（页眉 / 侧栏）与跨页面通用文案，含演示模式与四态场景名（FR-159）
export const common = {
  consoleName: '第二版管理台',
  demoMode: '演示模式',
  mockBuilding: 'mock 建设中',
  // 侧栏底部操作人角色
  superAdmin: '超级管理员',
  // 页眉刷新按钮
  refresh: '刷新',
  // 页眉控制面在线状态药丸
  controlPlaneOnline: '控制面在线',
  scenario: {
    label: '数据场景',
    empty: '空态',
    normal: '常规',
    huge: '超大量',
    error: '异常',
  },
  // 顶栏 env 过滤器（FR-178）：按 env 过滤各页视图的作用域
  envFilter: {
    label: 'env 过滤器',
    all: '全部环境',
  },
  sidebar: {
    expand: '展开导航',
    collapse: '收起导航',
  },
} as const
