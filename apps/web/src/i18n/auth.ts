// 鉴权域文案（FR-179 登录鉴权）：品牌标题 / 表单标签 / 按钮 / 错误提示 + 登出 / 会话过期文案。
export const auth = {
  login: {
    // 品牌字标（与灯塔 mark 横向并排）
    title: 'Beacon',
    // 副标题：一句话说明这是做什么的
    subtitle: '登录以管理跨服控制面',
    // 用户名字段标签与占位
    username: '用户名',
    usernamePlaceholder: '请输入用户名',
    // 口令字段标签与占位
    password: '口令',
    passwordPlaceholder: '请输入口令',
    // 主按钮：常态与提交中
    submit: '登录',
    submitting: '登录中…',
    // 前端校验：用户名或口令为空
    missingCredentials: '请输入用户名与口令',
    // 兜底登录失败文案（非结构化错误时使用；结构化错误优先展示后端脱敏 message）
    failed: '登录失败，请稍后重试',
    // 卡片底部环境副文案
    envHint: '第二版管理台 · 演示环境',
  },
  logout: {
    // 页眉登出按钮
    action: '登出',
  },
  // 会话过期（令牌失效被踢回登录页时的提示语，供未来提示使用）
  sessionExpired: '登录已过期，请重新登录',
} as const
