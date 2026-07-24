// 公共域文案：Shell（页眉 / 侧栏）与跨页面通用文案，含演示模式与四态场景名（FR-159）
export const common = {
  consoleName: '管理台',
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
  // 顶栏环境过滤器（FR-178 / FR-192）：按环境过滤各页视图的作用域
  envFilter: {
    label: '环境过滤器',
    all: '全部环境',
    badgeAll: '全部',
    badgeEnv: '环境',
  },
  // 站内开源协议页（FR-190）：项目 MIT + 运行时第三方依赖协议清单
  license: {
    pageTitle: '开源协议',
    intro: 'Beacon 基于以下开源软件包构建。',
    project: '项目',
    spdx: '许可证',
    copyright: '版权声明',
    repository: '源码仓库',
    fullTextTitle: '项目协议正文（MIT License）',
    viewOnGithub: '在 GitHub 查看 LICENSE 原文',
    depsTitle: '运行时依赖（{{count}}）',
    depsIntro:
      'Beacon 基于以下开源软件包构建。清单含管理台 npm 运行时 {{npm}} 项与控制面 Go 模块 {{go}} 项（生成于 {{date}}）；不含纯开发/测试工具。业务插件与第三方按其自身许可证执行。',
    searchPlaceholder: '搜索软件包…',
    noMatch: '没有匹配的软件包',
    groupRuntime: '运行时依赖（{{count}}）',
    groupNpm: '管理台依赖（npm · {{count}}）',
    groupGo: '控制面依赖（Go · {{count}}）',
    colPackage: '软件包',
    colVersion: '版本',
    colLicense: '协议',
    colAuthor: '作者',
  },
  sidebar: {
    expand: '展开导航',
    collapse: '收起导航',
    openMobile: '打开导航',
    closeMobile: '关闭导航',
    closeMobileOverlay: '关闭导航遮罩',
    license: '开源协议',
  },
  // 双段页眉（FR-187）+ 搜索/语言/通知（FR-193～195）
  header: {
    accountMenu: '账户菜单',
    search: '搜索',
    language: '语言',
    notifications: '通知',
    comingSoon: '即将推出',
    langZh: '中文',
    langEn: 'English',
    notificationsEmpty: '暂无未处理告警',
    notificationsLoading: '加载中…',
    notificationsError: '通知加载失败',
    viewAllAlerts: '查看全部告警',
    // 页眉一键处理（不另开备注框；已处理用固定短备注）
    quickAck: '确认',
    quickResolve: '已处理',
    quickResolveNote: '页眉一键标记已处理',
  },
  // 命令面板（FR-193）
  commandPalette: {
    title: '全局搜索',
    placeholder: '搜索页面、服务器、审计动作…',
    empty: '无匹配结果',
    hint: 'Ctrl+K 打开 · Esc 关闭',
    hintNav: '↑↓ 选择 · Enter 跳转',
    groupNav: '导航',
    groupServers: '服务器',
    groupAudits: '审计动作',
    searchServers: '在服务器中搜索「{{q}}」',
  },
  // 段 1 指标条（FR-188）
  metrics: {
    placeholderHint: '全局指标加载中…',
    controlPlaneOffline: '控制面异常',
    controlPlaneUnknown: '控制面状态未知',
    agentOnline: 'Agent 在线',
    pendingRegistrations: '待确认',
    openAlerts: '未处理告警',
    activeChanges: '进行中变更单',
  },
  // 表格勾选行无障碍标签
  selectRow: '选择 {{id}}',
} as const
