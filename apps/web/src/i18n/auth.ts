// 鉴权域文案（FR-179 登录页）：品牌标题 / 表单标签 / 按钮 / 提示文案。
// 阶段 A 仅登录页 mockup，暂不含登出、会话过期等阶段 B 文案。
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
    // mockup 成功提示（不真实跳转，仅演示成功态）
    successMock: '登录成功（mockup 演示，未接真实鉴权）',
    // mockup 失败提示（口令含 “bad” 触发，演示脱敏错误态）
    errorMock: '用户名或口令有误，请重试',
    // 卡片底部环境副文案
    envHint: '第二版管理台 · 演示环境',
    // 提示演示者如何触发失败态
    mockTip: '提示：口令含 “bad” 演示失败态，其余演示成功态',
  },
} as const
