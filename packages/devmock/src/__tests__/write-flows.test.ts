// 写操作闭环与场景重置：UI 操作后列表状态真的变化；切换场景 / 重置后回到初始数据；
// 非法状态迁移按规格回 409。
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { resetMockData, setMockScenario } from '../index'
import { callJson, resetMockWorld, server } from './msw-server'

interface IdentityItem {
  identityId: string
  serverId: string | null
  status: string
}

// 提审测试用幂等键（对齐后端 validIdempotencyKey：非空、≤64、可打印 ASCII）
const SUBMIT_KEY = 'test-idem-key-0001'

async function findIdentity(serverId: string): Promise<IdentityItem | undefined> {
  const { json } = await callJson('GET', `/admin/v2/agent-identities?keyword=${serverId}&pageSize=100`)
  const paged = json as { items: IdentityItem[] }
  return paged.items.find((item) => item.serverId === serverId)
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  resetMockWorld()
})
afterAll(() => {
  server.close()
})

describe('身份确认闭环（approve）', () => {
  it('approve 待确认身份后列表状态变化，且未分配篮出现新 server', async () => {
    const pending = await findIdentity('game-new-1')
    expect(pending?.status).toBe('pending')

    const before = (await callJson('GET', '/admin/v2/servers?assigned=false&namespaceId=1&pageSize=100')).json as {
      items: { serverId: string }[]
      total: number
    }
    expect(before.items.some((s) => s.serverId === 'game-new-1')).toBe(false)

    const approve = await callJson('POST', `/admin/v2/agent-identities/${pending?.identityId ?? ''}/approve`, {
      serverId: 'game-new-1',
    })
    expect(approve.status).toBe(200)

    const after = await findIdentity('game-new-1')
    expect(after?.status).toBe('active')

    const basket = (await callJson('GET', '/admin/v2/servers?assigned=false&namespaceId=1&pageSize=100')).json as {
      items: { serverId: string }[]
    }
    expect(basket.items.some((s) => s.serverId === 'game-new-1')).toBe(true)
  })

  it('非法迁移返回 409：对 active 身份再 approve', async () => {
    const active = await findIdentity('game-1')
    expect(active?.status).toBe('active')
    const { status, json } = await callJson('POST', `/admin/v2/agent-identities/${active?.identityId ?? ''}/approve`, {})
    expect(status).toBe(409)
    expect((json as { code: string }).code).toBe('illegal_state')
  })

  it('原因必填的写操作缺原因返回 400', async () => {
    const pending = await findIdentity('game-new-1')
    const { status, json } = await callJson('POST', `/admin/v2/agent-identities/${pending?.identityId ?? ''}/reject`, {})
    expect(status).toBe(400)
    expect((json as { code: string }).code).toBe('missing_reason')
  })

  it('占用冲突（Q3）未勾选强制解绑的 approve 被 409 拒绝', async () => {
    const { json } = await callJson('GET', '/admin/v2/agent-identities?status=pending&pageSize=100')
    const paged = json as { items: (IdentityItem & { conflictReason: string | null })[] }
    const occupied = paged.items.find((item) => item.conflictReason === 'server-id-occupied')
    expect(occupied).toBeDefined()
    const denied = await callJson('POST', `/admin/v2/agent-identities/${occupied?.identityId ?? ''}/approve`, {
      serverId: 'lobby-2',
    })
    expect(denied.status).toBe(409)
    expect((denied.json as { code: string }).code).toBe('server_id_occupied')
    const forced = await callJson('POST', `/admin/v2/agent-identities/${occupied?.identityId ?? ''}/approve`, {
      serverId: 'lobby-2',
      forceUnbindOccupier: true,
    })
    expect(forced.status).toBe(200)
    // 旧占用身份被解绑
    const old = await findIdentity('lobby-2')
    expect(old?.status).toBe('unbound')
  })
})

describe('审批申请写闭环', () => {
  it('消息 payload 专用入口创建统一审批申请', async () => {
    const created = await callJson('POST', '/admin/v2/messages/msg-901/payload/approval-requests', {
      reason: '排查异常链路失败样本',
    })

    expect(created.status).toBe(202)
    expect(created.json).toMatchObject({
      operationKey: 'message.payload.read',
      resourceType: 'message',
      resourceId: 'msg-901',
      requestReason: '排查异常链路失败样本',
      status: 'pending',
    })
  })

  it('批准待审批申请时按路径参数定位并记录 executing 时间线', async () => {
    const requestId = 'apr_change_9101'
    const detail = await callJson('GET', `/admin/v2/approval-requests/${requestId}`)
    expect(detail.status).toBe(200)

    const approved = await callJson('POST', `/admin/v2/approval-requests/${requestId}/approve`, {
      decisionNote: '演示批准',
    })
    expect(approved.status).toBe(202)
    expect((approved.json as { status: string }).status).toBe('executing')

    const after = await callJson('GET', `/admin/v2/approval-requests/${requestId}`)
    const timeline = (after.json as { timeline: { type: string }[] }).timeline
    expect(timeline.at(-1)?.type).toBe('executing')
  })
})

describe('身份地址覆盖闭环（FR-204）', () => {
  it('仅 active endpoint 可设置覆盖；原因必填，清除覆盖恢复自动探测', async () => {
    const identity = await findIdentity('proxy-1')
    const detail = await callJson('GET', `/admin/v2/agent-identities/${identity?.identityId ?? ''}`)
    const endpoints = (detail.json as { endpoints: { endpointKey: string }[] }).endpoints
    expect(endpoints).toHaveLength(2)

    const missing = await callJson(
      'PUT',
      `/admin/v2/agent-identities/${identity?.identityId ?? ''}/endpoints/${encodeURIComponent(endpoints[0].endpointKey)}`,
      { overrideAddress: '198.51.100.20:25577' },
    )
    expect(missing.status).toBe(400)
    expect((missing.json as { code: string }).code).toBe('missing_reason')

    const updated = await callJson(
      'PUT',
      `/admin/v2/agent-identities/${identity?.identityId ?? ''}/endpoints/${encodeURIComponent(endpoints[0].endpointKey)}`,
      { overrideAddress: '198.51.100.20:25577', reason: '公网入口地址修正' },
    )
    expect(updated.status).toBe(200)
    expect((updated.json as { effectiveAddress: string; source: string }).effectiveAddress).toBe('198.51.100.20:25577')
    expect((updated.json as { source: string }).source).toBe('override')

    const cleared = await callJson(
      'PUT',
      `/admin/v2/agent-identities/${identity?.identityId ?? ''}/endpoints/${encodeURIComponent(endpoints[0].endpointKey)}`,
      { overrideAddress: null, reason: '恢复自动探测地址' },
    )
    expect(cleared.status).toBe(200)
    expect((cleared.json as { source: string }).source).toBe('detected')

    const inactiveIdentity = await findIdentity('proxy-2')
    const inactiveDetail = await callJson('GET', `/admin/v2/agent-identities/${inactiveIdentity?.identityId ?? ''}`)
    const inactive = (inactiveDetail.json as { endpoints: { endpointKey: string; active: boolean }[] }).endpoints.find(
      (endpoint) => !endpoint.active,
    )
    const denied = await callJson(
      'PUT',
      `/admin/v2/agent-identities/${inactiveIdentity?.identityId ?? ''}/endpoints/${encodeURIComponent(inactive?.endpointKey ?? '')}`,
      { overrideAddress: '198.51.100.20:25578', reason: '不得修改失活监听' },
    )
    expect(denied.status).toBe(409)
    const inactiveClear = await callJson(
      'PUT',
      `/admin/v2/agent-identities/${inactiveIdentity?.identityId ?? ''}/endpoints/${encodeURIComponent(inactive?.endpointKey ?? '')}`,
      { overrideAddress: null, reason: '清除失活监听残留覆盖' },
    )
    expect(inactiveClear.status).toBe(200)
  })
})

describe('场景切换与重置', () => {
  it('切换场景后数据集重置：approve 的变更不残留', async () => {
    const pending = await findIdentity('game-new-1')
    await callJson('POST', `/admin/v2/agent-identities/${pending?.identityId ?? ''}/approve`, { serverId: 'game-new-1' })
    expect((await findIdentity('game-new-1'))?.status).toBe('active')

    setMockScenario('huge')
    setMockScenario('normal')
    expect((await findIdentity('game-new-1'))?.status).toBe('pending')
  })

  it('resetMockData 直接重建当前场景数据', async () => {
    const pending = await findIdentity('game-new-1')
    await callJson('POST', `/admin/v2/agent-identities/${pending?.identityId ?? ''}/approve`, { serverId: 'game-new-1' })
    resetMockData()
    expect((await findIdentity('game-new-1'))?.status).toBe('pending')
  })

  it('未分配身份必须在 approve 请求中显式提供 serverId', async () => {
    const { json } = await callJson('GET', '/admin/v2/agent-identities?status=pending&pageSize=100')
    const pending = (json as { items: IdentityItem[] }).items.find((item) => item.serverId === null)
    expect(pending).toBeDefined()

    const missing = await callJson('POST', `/admin/v2/agent-identities/${pending?.identityId ?? ''}/approve`, {})
    expect(missing.status).toBe(400)
    expect((missing.json as { code: string }).code).toBe('invalid_param')
  })
})

describe('Legacy 运维设置契约', () => {
  it('返回服务端白名单对应的 40 项设置并包含新增 8 项', async () => {
    const { status, json } = await callJson('GET', '/admin/v1/settings')
    const items = (json as { items: { key: string }[] }).items
    const keys = new Set(items.map((item) => item.key))
    const newlySyncedKeys = [
      'undo.window-hours',
      'identity.conflict-window-sec',
      'delivery.approver-separation-enabled',
      'delivery.blob-retention-days',
      'delivery.blob-capacity-bytes',
      'delivery.upload-concurrency',
      'delivery.download-concurrency',
      'delivery.cleanup-interval-minutes',
    ]

    expect(status).toBe(200)
    expect(items).toHaveLength(40)
    for (const key of newlySyncedKeys) {
      expect(keys.has(key), `缺少设置项 ${key}`).toBe(true)
    }
  })
})

describe('变更单生命周期闭环（202 票据 + 统一审批驱动）', () => {
  // 六类申请动作返回票据：{approvalRequestId, status, operationKey, orderId}
  interface Ticket {
    approvalRequestId: string
    status: string
    operationKey: string
    orderId: number
    impactSummary: { targetCount: number; batchCount: number; payloadFiles: number; payloadConfigs: number }
  }

  /** 在审批中心批准某张申请（真机由审批 worker 回投领域副作用，mock 同路径） */
  async function approveTicket(requestId: string): Promise<void> {
    const approved = await callJson('POST', `/admin/v2/approval-requests/${requestId}/approve`, {})
    expect(approved.status).toBe(202)
  }

  async function orderStatus(orderId: number): Promise<string> {
    const detail = await callJson('GET', `/admin/v2/change-orders/${String(orderId)}`)
    return (detail.json as { status: string }).status
  }

  it('创建 → 提审（202 票据）→ 审批 → 启动 → 批次放行（202）→ 审批 → 完成 → 回滚（202）→ 审批', async () => {
    const created = await callJson('POST', '/admin/v2/change-orders', {
      namespaceId: 1,
      title: '测试专用小流量变更',
      sourceServerId: 'game-1',
      selector: { servers: ['pvp-1'] },
    })
    expect(created.status).toBe(201)
    const orderId = (created.json as { id: number }).id

    // 提审：202 + 票据（本单唯一的统一审批申请）
    const submitted = await callJson(
      'POST',
      `/admin/v2/change-orders/${String(orderId)}/submit`,
      { reason: '演示提审' },
      { 'Idempotency-Key': SUBMIT_KEY },
    )
    expect(submitted.status).toBe(202)
    const submitTicket = submitted.json as Ticket
    expect(submitTicket.operationKey).toBe('delivery.approve')
    expect(submitTicket.status).toBe('pending')
    expect(submitTicket.orderId).toBe(orderId)
    expect(submitTicket.impactSummary.targetCount).toBe(1)
    expect(await orderStatus(orderId)).toBe('pending_approval')

    // 票据号必须能读到审批详情（两域同号）
    const approvalDetail = await callJson('GET', `/admin/v2/approval-requests/${submitTicket.approvalRequestId}`)
    expect(approvalDetail.status).toBe(200)
    expect((approvalDetail.json as { resourceId: string }).resourceId).toBe(String(orderId))

    // 批准即启动灰度（真机批准 worker 直接启动，无第二次启动入口）
    await approveTicket(submitTicket.approvalRequestId)
    expect(await orderStatus(orderId)).toBe('rolling')
    const started = await callJson('GET', `/admin/v2/change-orders/${String(orderId)}`)
    const startedOrder = started.json as { batches: { batchNo: number; status: string }[] }
    const awaiting = startedOrder.batches.find((b) => b.status === 'awaiting_confirm')
    expect(awaiting).toBeDefined()

    // 批次放行：202 票据，单据状态不在此刻变化
    const confirmed = await callJson(
      'POST',
      `/admin/v2/change-orders/${String(orderId)}/batches/${String(awaiting?.batchNo ?? 0)}/confirm`,
      undefined,
      { 'Idempotency-Key': SUBMIT_KEY },
    )
    expect(confirmed.status).toBe(202)
    const confirmTicket = confirmed.json as Ticket
    expect(confirmTicket.operationKey).toBe('delivery.confirm_batch')
    expect(await orderStatus(orderId)).toBe('rolling')

    // 审批通过后放行：末批确认即完成
    await approveTicket(confirmTicket.approvalRequestId)
    expect(await orderStatus(orderId)).toBe('completed')

    // 整单回滚：202 票据，批准后才进入回滚
    const rolledBack = await callJson(
      'POST',
      `/admin/v2/change-orders/${String(orderId)}/rollback`,
      { reason: '演示回滚' },
      { 'Idempotency-Key': SUBMIT_KEY },
    )
    expect(rolledBack.status).toBe(202)
    const rollbackTicket = rolledBack.json as Ticket
    expect(rollbackTicket.operationKey).toBe('delivery.rollback')
    expect(await orderStatus(orderId)).toBe('completed')

    await approveTicket(rollbackTicket.approvalRequestId)
    expect(await orderStatus(orderId)).toBe('rolled_back')
  })

  it('六类申请动作都要求幂等键（缺键 400 INVALID_PARAM，且状态不变）', async () => {
    const created = await callJson('POST', '/admin/v2/change-orders', {
      namespaceId: 1,
      title: '缺幂等键拒绝演示',
      selector: { servers: ['pvp-1'] },
    })
    const orderId = (created.json as { id: number }).id

    // 提审缺键 → 400 且仍是 draft（mock 刻意不制造真机的两步卡死）
    const submitNoKey = await callJson('POST', `/admin/v2/change-orders/${String(orderId)}/submit`, { reason: '缺键提审' })
    expect(submitNoKey.status).toBe(400)
    expect((submitNoKey.json as { code: string }).code).toBe('INVALID_PARAM')
    expect(await orderStatus(orderId)).toBe('draft')

    // 补齐键 → 202 票据
    const submitOk = await callJson(
      'POST',
      `/admin/v2/change-orders/${String(orderId)}/submit`,
      { reason: '补键提审' },
      { 'Idempotency-Key': SUBMIT_KEY },
    )
    expect(submitOk.status).toBe(202)

    // 状态非法先于键判定（与真机 RequestDelete 同序）：非 draft 单删申请 → 409
    const deleteNonDraft = await callJson('DELETE', `/admin/v2/change-orders/${String(orderId)}`, {
      reason: '状态非法删除',
    })
    expect(deleteNonDraft.status).toBe(409)

    // 新建 draft 单：删除申请缺键 → 400 INVALID_PARAM，且单据仍在
    const draft = await callJson('POST', '/admin/v2/change-orders', {
      namespaceId: 1,
      title: '缺键删除演示',
      selector: { servers: ['pvp-1'] },
    })
    const draftId = (draft.json as { id: number }).id
    const deleteNoKey = await callJson('DELETE', `/admin/v2/change-orders/${String(draftId)}`, { reason: '缺键删除' })
    expect(deleteNoKey.status).toBe(400)
    expect((deleteNoKey.json as { code: string }).code).toBe('INVALID_PARAM')
    expect(await orderStatus(draftId)).toBe('draft')

    // 补键 → 202 票据（删除也是申请动作）
    const deleteOk = await callJson(
      'DELETE',
      `/admin/v2/change-orders/${String(draftId)}`,
      { reason: '补键删除' },
      { 'Idempotency-Key': SUBMIT_KEY },
    )
    expect(deleteOk.status).toBe(202)
    expect((deleteOk.json as Ticket).operationKey).toBe('delivery.draft_delete')
  })

  it('提审缺原因（含纯空白）被 400 拒绝，补原因与幂等键后可正常提审', async () => {
    const created = await callJson('POST', '/admin/v2/change-orders', {
      namespaceId: 1,
      title: '缺原因提审演示',
      selector: { servers: ['pvp-1'] },
    })
    const orderId = (created.json as { id: number }).id
    const path = `/admin/v2/change-orders/${String(orderId)}/submit`
    const key = { 'Idempotency-Key': SUBMIT_KEY }

    // 无 body 与纯空白原因都按「原因不能为空」拒绝（对齐后端 RequestSubmit）
    const noBody = await callJson('POST', path)
    expect(noBody.status).toBe(400)
    expect((noBody.json as { code: string }).code).toBe('approval_reason_required')

    const blank = await callJson('POST', path, { reason: '   ' })
    expect(blank.status).toBe(400)
    expect((blank.json as { code: string }).code).toBe('approval_reason_required')

    // 原因齐备但缺幂等键 → 400 INVALID_PARAM（对齐后端 validIdempotencyKey 前置校验）
    const noKey = await callJson('POST', path, { reason: '缺幂等键提审' })
    expect(noKey.status).toBe(400)
    expect((noKey.json as { code: string }).code).toBe('INVALID_PARAM')

    // 键超长（>64）同样按非法键拒绝
    const longKey = await callJson('POST', path, { reason: '超长键提审' }, { 'Idempotency-Key': 'k'.repeat(65) })
    expect(longKey.status).toBe(400)
    expect((longKey.json as { code: string }).code).toBe('INVALID_PARAM')

    // 被拒后仍是 draft，补原因 + 幂等键即提审成功（202 票据）
    const ok = await callJson('POST', path, { reason: '补原因提审' }, key)
    expect(ok.status).toBe(202)
    expect((ok.json as Ticket).status).toBe('pending')
    expect(await orderStatus(orderId)).toBe('pending_approval')

    // 判定顺序与真机同序（先 reason 再状态）：非 draft 单缺原因仍是 400，而不是 409
    const nonDraftNoReason = await callJson('POST', path)
    expect(nonDraftNoReason.status).toBe(400)
    expect((nonDraftNoReason.json as { code: string }).code).toBe('approval_reason_required')

    // 有原因而状态非法，才落到 409（状态判定先于幂等键判定）
    const nonDraftWithReason = await callJson('POST', path, { reason: '重复提审' }, key)
    expect(nonDraftWithReason.status).toBe(409)
    expect((nonDraftWithReason.json as { code: string }).code).toBe('illegal_state')
  })

  it('目标集与活动单交叠时启动被跳过（冲突守卫在审批通过后生效）', async () => {
    // game-1 属于常规态 rolling 单的目标集：提审 + 批准后不启动，继续停在待审批
    const created = await callJson('POST', '/admin/v2/change-orders', {
      namespaceId: 1,
      title: '冲突守卫演示',
      selector: { servers: ['game-1'] },
    })
    const orderId = (created.json as { id: number }).id
    const submitted = await callJson(
      'POST',
      `/admin/v2/change-orders/${String(orderId)}/submit`,
      { reason: '冲突守卫演示提审' },
      { 'Idempotency-Key': SUBMIT_KEY },
    )
    expect(submitted.status).toBe(202)
    const ticket = submitted.json as Ticket

    await approveTicket(ticket.approvalRequestId)
    // 目标集交叠 → 领域侧不启动（保持待审批），不留半启动状态
    expect(await orderStatus(orderId)).toBe('pending_approval')
  })

  it('旧审批 / 撤回 / 启动入口一律 403（审批决定只在审批中心）', async () => {
    const draft = await callJson('POST', '/admin/v2/change-orders', {
      namespaceId: 1,
      title: '废弃入口演示',
      selector: { servers: ['pvp-1'] },
    })
    const orderId = (draft.json as { id: number }).id
    for (const action of ['approve', 'reject', 'withdraw', 'start']) {
      const res = await callJson('POST', `/admin/v2/change-orders/${String(orderId)}/${action}`, { reason: '演示' })
      expect(res.status, `${action} 应 403`).toBe(403)
      expect((res.json as { code: string }).code).toBe('FORBIDDEN')
    }
  })

  it('文件内容预览恒 409 operation_requires_approval（内容读取必须先走审批）', async () => {
    const list = (await callJson('GET', '/admin/v2/change-orders?status=rolling')).json as {
      items: { id: number }[]
    }
    const orderId = list.items[0].id
    const detail = await callJson('GET', `/admin/v2/change-orders/${String(orderId)}`)
    const fileItem = (detail.json as { items: { id: number; kind: string }[] }).items.find((i) => i.kind === 'file_diff')
    expect(fileItem).toBeDefined()

    const res = await callJson(
      'GET',
      `/admin/v2/change-orders/${String(orderId)}/items/${String(fileItem?.id ?? 0)}/file-diff`,
    )
    expect(res.status).toBe(409)
    expect((res.json as { code: string }).code).toBe('operation_requires_approval')
  })

  it('draft 以外状态删除申请按状态机拒绝', async () => {
    const rollingList = (await callJson('GET', '/admin/v2/change-orders?status=rolling')).json as {
      items: { id: number }[]
    }
    const rollingId = rollingList.items[0].id
    const del = await callJson(
      'DELETE',
      `/admin/v2/change-orders/${String(rollingId)}`,
      { reason: '状态非法删除演示' },
      { 'Idempotency-Key': SUBMIT_KEY },
    )
    expect(del.status).toBe(409)
  })
})

describe('配置版本链写闭环', () => {
  async function essentialsFileId(): Promise<number> {
    const files = (await callJson('GET', '/admin/v2/config-files?namespaceId=1&keyword=Essentials')).json as {
      items: { id: number; name: string }[]
    }
    const file = files.items.find((f) => f.name === 'plugins/Essentials/config.yml')
    if (!file) {
      throw new Error('fixture 缺失 Essentials 配置文件')
    }
    return file.id
  }

  it('保存新版本：过期基线 409，正确基线 201 并推进版本号', async () => {
    const fileId = await essentialsFileId()
    const versions = (
      await callJson('GET', `/admin/v2/config-files/${String(fileId)}/versions?scopeLevel=namespace&scopeRefId=1`)
    ).json as { items: { versionId: number; versionNo: number }[] }
    const head = versions.items[0]

    const stale = await callJson('POST', `/admin/v2/config-files/${String(fileId)}/versions`, {
      scopeLevel: 'namespace',
      scopeRefId: 1,
      content: 'teleport-cooldown: 9',
      basedOnVersionId: 12345,
    })
    expect(stale.status).toBe(409)
    expect((stale.json as { code: string }).code).toBe('CONFIG_VERSION_CONFLICT')

    const saved = await callJson('POST', `/admin/v2/config-files/${String(fileId)}/versions`, {
      scopeLevel: 'namespace',
      scopeRefId: 1,
      content: 'teleport-cooldown: 9\nspawn-on-join: false\neconomy-enabled: true\nmotd: 欢迎来到 Beacon 集群',
      remark: '压测调参',
      basedOnVersionId: head.versionId,
    })
    expect(saved.status).toBe(201)
    expect((saved.json as { versionNo: number }).versionNo).toBe(head.versionNo + 1)
  })

  it('回收站闭环：删除 → 常规列表消失 → 恢复', async () => {
    const fileId = await essentialsFileId()
    expect((await callJson('DELETE', `/admin/v2/config-files/${String(fileId)}`)).status).toBe(204)
    const list = (await callJson('GET', '/admin/v2/config-files?namespaceId=1&keyword=Essentials')).json as {
      items: { id: number }[]
    }
    expect(list.items.some((f) => f.id === fileId)).toBe(false)
    const trash = (await callJson('GET', '/admin/v2/config-files/trash?namespaceId=1')).json as {
      items: { id: number }[]
    }
    expect(trash.items.some((f) => f.id === fileId)).toBe(true)
    expect((await callJson('POST', `/admin/v2/config-files/${String(fileId)}/restore`)).status).toBe(200)
  })
})

describe('健康权重热更', () => {
  it('非法阈值 400，合法配置产生新 rev', async () => {
    const current = (await callJson('GET', '/admin/v2/settings/health-weights')).json as {
      current: { rev: number; config: { weights: Record<string, number>; normalize: Record<string, number>; levels: { healthyMin: number; degradedMin: number } } }
    }
    const bad = await callJson('PUT', '/admin/v2/settings/health-weights', {
      ...current.current.config,
      levels: { healthyMin: 40, degradedMin: 50 },
    })
    expect(bad.status).toBe(400)

    const good = await callJson('PUT', '/admin/v2/settings/health-weights', current.current.config)
    expect(good.status).toBe(200)
    expect((good.json as { current: { rev: number } }).current.rev).toBe(current.current.rev + 1)
  })
})

describe('env 展示维度写闭环（FR-178）', () => {
  interface EnvItemShape {
    id: number
    name: string
    description: string
    namespaceCount: number
  }

  async function listEnvs(): Promise<EnvItemShape[]> {
    const { json } = await callJson('GET', '/admin/v2/envs?pageSize=100')
    return (json as { items: EnvItemShape[] }).items
  }

  it('建 env → 抢占已占用 namespace 409 指明冲突方 → 删占用 env 释放后可映射', async () => {
    // 常规态已有「生产」映射 prod(ns=1)、「测试」映射 test(ns=2)
    const created = await callJson('POST', '/admin/v2/envs', { name: '预发布', description: '演示新增' })
    expect(created.status).toBe(201)
    const envId = (created.json as EnvItemShape).id
    expect((await listEnvs()).some((e) => e.name === '预发布')).toBe(true)

    // 抢占 prod(ns=1)（已属「生产」）→ 409 且 message 指明冲突方
    const conflict = await callJson('PUT', `/admin/v2/envs/${String(envId)}/namespaces`, { namespaceIds: [1] })
    expect(conflict.status).toBe(409)
    const body = conflict.json as { code: string; message: string }
    expect(body.code).toBe('ENV_NAMESPACE_CONFLICT')
    expect(body.message).toContain('生产')

    // 删「生产」env → 释放 ns=1，之后可映射
    const prod = (await listEnvs()).find((e) => e.name === '生产')
    expect((await callJson('DELETE', `/admin/v2/envs/${String(prod?.id ?? 0)}`)).status).toBe(204)
    const remap = await callJson('PUT', `/admin/v2/envs/${String(envId)}/namespaces`, { namespaceIds: [1] })
    expect(remap.status).toBe(200)
    expect((remap.json as EnvItemShape).namespaceCount).toBe(1)
  })

  it('同名 env 冲突 409；PATCH 局部改描述生效', async () => {
    const dup = await callJson('POST', '/admin/v2/envs', { name: '生产' })
    expect(dup.status).toBe(409)
    expect((dup.json as { code: string }).code).toBe('ENV_CONFLICT')

    const test = (await listEnvs()).find((e) => e.name === '测试')
    const patched = await callJson('PATCH', `/admin/v2/envs/${String(test?.id ?? 0)}`, { description: '改后描述' })
    expect(patched.status).toBe(200)
    expect((patched.json as { description: string }).description).toBe('改后描述')
    expect((patched.json as { name: string }).name).toBe('测试')
  })

  it('整体替换语义：去重 + 幂等', async () => {
    // 先删「生产」释放 ns=1，再把「测试」整体替换到 [1,1,2]（去重成 [1,2]）
    const prod = (await listEnvs()).find((e) => e.name === '生产')
    await callJson('DELETE', `/admin/v2/envs/${String(prod?.id ?? 0)}`)
    const test = (await listEnvs()).find((e) => e.name === '测试')
    const envId = test?.id ?? 0
    const replaced = await callJson('PUT', `/admin/v2/envs/${String(envId)}/namespaces`, { namespaceIds: [1, 1, 2] })
    expect(replaced.status).toBe(200)
    expect((replaced.json as EnvItemShape).namespaceCount).toBe(2)
    const again = await callJson('PUT', `/admin/v2/envs/${String(envId)}/namespaces`, { namespaceIds: [1, 2] })
    expect((again.json as EnvItemShape).namespaceCount).toBe(2)
  })

  it('待映射 namespace 不存在返回 400', async () => {
    const test = (await listEnvs()).find((e) => e.name === '测试')
    const bad = await callJson('PUT', `/admin/v2/envs/${String(test?.id ?? 0)}/namespaces`, { namespaceIds: [9999] })
    expect(bad.status).toBe(400)
    expect((bad.json as { code: string }).code).toBe('ENV_NAMESPACE_NOT_FOUND')
  })

  it('双名称创建、检索与仅展示名更新保持稳定 code', async () => {
    const created = await callJson('POST', '/admin/v2/envs', { code: 'preprod', displayName: '预发布环境' })
    expect(created.status).toBe(201)
    const item = created.json as EnvItemShape & { code: string; displayName: string }
    expect(item).toMatchObject({ name: 'preprod', code: 'preprod', displayName: '预发布环境' })
    expect((await callJson('GET', '/admin/v2/envs?keyword=预发布环境')).json).toMatchObject({ total: 1 })
    expect((await callJson('GET', '/admin/v2/envs?keyword=preprod')).json).toMatchObject({ total: 1 })
    const renamed = await callJson('PATCH', `/admin/v2/envs/${String(item.id)}`, { displayName: '预发布新名' })
    expect(renamed.json).toMatchObject({ code: 'preprod', displayName: '预发布新名' })
    const immutable = await callJson('PATCH', `/admin/v2/envs/${String(item.id)}`, { name: 'changed' })
    expect((immutable.json as { code: string }).code).toBe('IMMUTABLE_IDENTIFIER')
    const ambiguous = await callJson('POST', '/admin/v2/envs', { name: 'old', code: 'new' })
    expect((ambiguous.json as { code: string }).code).toBe('AMBIGUOUS_IDENTIFIER')
  })

  it('namespace 双名称创建、检索与仅展示名更新保持稳定 code', async () => {
    const created = await callJson('POST', '/admin/v2/namespaces', { code: 'preprod-ns', displayName: '预发布命名空间' })
    expect(created.status).toBe(201)
    const item = created.json as { id: number; code: string; displayName: string }
    expect(item).toMatchObject({ code: 'preprod-ns', displayName: '预发布命名空间' })
    expect((await callJson('GET', '/admin/v2/namespaces?keyword=预发布命名空间')).json).toMatchObject({ total: 1 })
    expect((await callJson('GET', '/admin/v2/namespaces?keyword=preprod-ns')).json).toMatchObject({ total: 1 })
    const renamed = await callJson('PATCH', `/admin/v2/namespaces/${String(item.id)}`, { displayName: '预发布新名' })
    expect(renamed.json).toMatchObject({ code: 'preprod-ns', displayName: '预发布新名' })
    const immutable = await callJson('PATCH', `/admin/v2/namespaces/${String(item.id)}`, { code: 'changed' })
    expect((immutable.json as { code: string }).code).toBe('IMMUTABLE_IDENTIFIER')
  })
})
