// 新服接入引导片段生成（FR-85 新服接入引导向导）。
//
// 按运维在向导填的身份字段，拼出可直接复制粘贴的两份文本：
//   - agent config.yml 的 identity 段（含中文注释，提示 zone 由控制面权威指派、勿在此配）；
//   - run 脚本的 env 段（BEACON_AGENT_IDENTITY_*，FR-41 环境变量覆盖、优先于本地 yaml）。
// 纯函数、无副作用、可穷举单测；不生成完整 config.yml（其它段沿用模板默认）。

// 接入身份输入：与 agent identity 字段一一对应（zone 不在此——由控制面指派）。
export interface OnboardingInput {
  // 环境（namespace）：必须与控制面 namespace 一致
  namespace: string
  // 本机唯一身份，环境内唯一
  serverId: string
  // 大区提示（group-hint）：尚未被控制面分配 zone 时的兜底 group
  group: string
  // 对外可达地址 ip:port
  address: string
}

// 生成结果：两份可复制文本
export interface OnboardingSnippets {
  // config.yml 的 identity 段（含注释）
  configYaml: string
  // run 脚本 env 段（BEACON_AGENT_IDENTITY_*）
  envScript: string
}

// 生成 config.yml identity 段 + env 段。值用双引号包裹（与 agent config.yml 模板一致）。
export function buildOnboardingSnippets(input: OnboardingInput): OnboardingSnippets {
  const { namespace, serverId, group, address } = input

  // config.yml 的 identity 段：与 agent/*/src/main/resources/config.yml 模板字段对齐；
  // 显式不含 zone（zone 由控制面权威指派，agent 不声明，见架构不变量 §6 / ADR-0004）。
  const configYaml = [
    'identity:',
    '  # 环境（namespace）：必须与控制面 namespace 一致。',
    `  namespace: "${namespace}"`,
    '  # 本机唯一身份，环境内唯一。必须显式配置，重复将被控制面拒绝。',
    `  server-id: "${serverId}"`,
    '  # 大区提示：仅在「该 serverId 尚未被控制面分配 zone」时作为兜底 group 使用。',
    `  group-hint: "${group}"`,
    '  # 对外可达地址 ip:port，用于注册上报与服务发现展示。',
    `  address: "${address}"`,
    '  # 注意：zone（小区）不在此配置——由 Beacon 控制面权威指派。',
  ].join('\n')

  // run 脚本 env 段：FR-41 环境变量覆盖，优先于本地 yaml；点分路径大写、点与连字符转下划线。
  const envScript = [
    `BEACON_AGENT_IDENTITY_NAMESPACE=${namespace}`,
    `BEACON_AGENT_IDENTITY_SERVER_ID=${serverId}`,
    `BEACON_AGENT_IDENTITY_GROUP_HINT=${group}`,
    `BEACON_AGENT_IDENTITY_ADDRESS=${address}`,
  ].join('\n')

  return { configYaml, envScript }
}
