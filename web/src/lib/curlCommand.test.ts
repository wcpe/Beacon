// admin API token「复制为 curl」纯逻辑单测（FR-90，见 ADR-0042）：
// 命令含认证头与只读样例端点；token / base 含特殊字符时 shell 安全引用。

import { describe, it, expect } from 'vitest'

import { buildApiKeyCurl } from './curlCommand'

describe('buildApiKeyCurl', () => {
  it('普通 token 生成带认证头、指向只读端点的 curl 命令', () => {
    const cmd = buildApiKeyCurl('bk_AbC123', { base: 'https://beacon.example.com/admin/v1' })
    // 含 curl 与认证头
    expect(cmd).toContain('curl')
    expect(cmd).toContain("-H 'X-Beacon-Api-Key: bk_AbC123'")
    // 指向只读样例端点 /system/status
    expect(cmd).toContain("'https://beacon.example.com/admin/v1/system/status'")
  })

  it('base 末尾斜杠被归一化，避免出现双斜杠', () => {
    const cmd = buildApiKeyCurl('bk_x', { base: 'https://h/admin/v1/' })
    expect(cmd).toContain("'https://h/admin/v1/system/status'")
    expect(cmd).not.toContain('v1//system')
  })

  it('token 含单引号时做 shell 安全转义，不破坏命令结构', () => {
    // 含单引号的恶意 / 异常 token，必须被安全引用（'\'' 收尾再拼）
    const cmd = buildApiKeyCurl("bk_a'b", { base: 'https://h/admin/v1' })
    expect(cmd).toContain("X-Beacon-Api-Key: bk_a'\\''b")
    // 不得出现裸露的、能提前闭合引号的片段
    expect(cmd).not.toContain("bk_a'b'")
  })

  it('缺省 base 时回退到相对前缀 /admin/v1', () => {
    const cmd = buildApiKeyCurl('bk_x', {})
    expect(cmd).toContain("'/admin/v1/system/status'")
  })
})
