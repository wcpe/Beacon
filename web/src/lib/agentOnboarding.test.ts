// agentOnboarding 纯函数单测（FR-85 新服接入引导向导）：
// 锁定生成的 agent config.yml identity 段与 run 脚本 env 段内容（按所填值填充、不含 zone）。
import { describe, it, expect } from 'vitest'
import { buildOnboardingSnippets } from './agentOnboarding'

const input = {
  namespace: 'prod',
  serverId: 'lobby-2',
  group: 'area1',
  address: '10.0.0.9:25565',
}

describe('buildOnboardingSnippets（FR-85 接入片段生成）', () => {
  it('config.yml identity 段含所填 namespace / server-id / group-hint / address', () => {
    const { configYaml } = buildOnboardingSnippets(input)
    expect(configYaml).toContain('namespace: "prod"')
    expect(configYaml).toContain('server-id: "lobby-2"')
    expect(configYaml).toContain('group-hint: "area1"')
    expect(configYaml).toContain('address: "10.0.0.9:25565"')
  })

  it('config.yml 片段不含 zone（zone 由控制面权威指派、agent 不声明）', () => {
    const { configYaml } = buildOnboardingSnippets(input)
    // 不出现 zone 字段（identity 下无 zone 键）
    expect(configYaml).not.toMatch(/^\s*zone:/m)
  })

  it('env 片段含 BEACON_AGENT_IDENTITY_* 四项且值与所填一致（FR-41 覆盖）', () => {
    const { envScript } = buildOnboardingSnippets(input)
    expect(envScript).toContain('BEACON_AGENT_IDENTITY_NAMESPACE=prod')
    expect(envScript).toContain('BEACON_AGENT_IDENTITY_SERVER_ID=lobby-2')
    expect(envScript).toContain('BEACON_AGENT_IDENTITY_GROUP_HINT=area1')
    expect(envScript).toContain('BEACON_AGENT_IDENTITY_ADDRESS=10.0.0.9:25565')
  })
})
