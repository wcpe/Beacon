/**
 * Mock 有状态写测试
 *
 * 自证假后端的写操作真实改内存态：后续读能看到变化（建 / 发布 / 回滚 / 删 各举一）。
 * 与 contract.test.ts 互补——契约管「端点不漏配」，本测试管「写后读一致」。
 */

import { beforeAll, describe, expect, it } from 'vitest'
import * as client from '../client'
import { enableMock } from './index'
import { setAuth } from '../../state/auth'

beforeAll(() => {
  enableMock()
  setAuth('mock-token', 'admin')
})

describe('mock 有状态：写操作后读能看到结果', () => {
  it('建配置 → 列表多一条且能取详情', async () => {
    const before = await client.listConfigs({ namespace: 'prod' })
    const created = await client.createConfig({
      namespace: 'prod',
      group: '__GLOBAL__',
      dataId: 'stateful-create.yml',
      scopeLevel: 'global',
      scopeTarget: '',
      format: 'yaml',
      content: 'created: true\n',
      comment: '有状态建',
    })
    const after = await client.listConfigs({ namespace: 'prod' })
    expect(after.length).toBe(before.length + 1)
    expect(after.some((c) => c.id === created.id)).toBe(true)

    const detail = await client.getConfig(created.id)
    expect(detail.content).toContain('created: true')
  })

  it('发布配置 → 版本 +1 且生效内容变', async () => {
    const detail = await client.getConfig(1)
    const r = await client.publishConfig(1, 'plugin:\n  name: published\n', '有状态发布')
    expect(r.version).toBe(detail.version + 1)

    const after = await client.getConfig(1)
    expect(after.version).toBe(r.version)
    expect(after.content).toContain('published')
  })

  it('回滚配置 → 回到旧版内容并生成新版本', async () => {
    const v1 = await client.getRevision(2, 1)
    const r = await client.rollbackConfig(2, 1, '有状态回滚')
    const after = await client.getConfig(2)
    // 回滚生成新版本号（指针前移），但内容回到 v1
    expect(after.version).toBe(r.version)
    expect(after.content).toBe(v1.content)
  })

  it('删除配置 → 列表中真没了', async () => {
    const created = await client.createConfig({
      namespace: 'prod',
      group: '__GLOBAL__',
      dataId: 'stateful-delete.yml',
      scopeLevel: 'global',
      scopeTarget: '',
      format: 'yaml',
      content: 'x: 1\n',
      comment: '待删',
    })
    await client.deleteConfig(created.id, '有状态删除')
    const after = await client.listConfigs({ namespace: 'prod' })
    expect(after.some((c) => c.id === created.id)).toBe(false)
  })

  it('zone 改派 → 指派与实例 zone 同步更新', async () => {
    await client.assignZone({
      namespace: 'prod',
      serverId: 'server-02',
      group: 'server-a',
      zone: 'zone-03',
      note: '有状态改派',
    })
    const assigns = await client.listAssignments('prod')
    expect(assigns.find((a) => a.serverId === 'server-02')?.zone).toBe('zone-03')
    const insts = await client.listInstances({ namespace: 'prod' })
    expect(insts.find((i) => i.serverId === 'server-02')?.zone).toBe('zone-03')
  })

  it('创建 API 密钥 → 列表可见且明文仅创建时返回', async () => {
    const created = await client.createApiKey({ name: '有状态密钥', role: 'readonly' })
    expect(created.key).not.toBe('')
    const list = await client.listApiKeys()
    const found = list.find((k) => k.id === created.id)
    expect(found).toBeDefined()
    // 列表视图剥离明文（ApiKeyView 无 key 字段）
    expect((found as unknown as { key?: string }).key).toBeUndefined()
  })
})
