// env 作用域单测（FR-178）：多 namespace 必须逐个受限请求，失效 env 与空映射均不得回退全量。
import { describe, expect, it, vi } from 'vitest'

import {
  fetchPagedItemsByEnvScope,
  resolveApiNamespaceId,
  resolveEnvNamespaceCodes,
  resolveEnvNamespaceScope,
  resolveObservationScopeNamespaceIds,
} from '../../features/env/use-env-scope'

describe('resolveEnvNamespaceScope', () => {
  const envs = [
    { id: 1, namespaces: [{ id: 11, name: 'prod' }] },
    { id: 2, namespaces: [{ id: 21, name: 'staging' }, { id: 22, name: 'dev' }] },
    { id: 3, namespaces: [] },
  ]

  it('全部环境保持不收窄', () => {
    expect(resolveEnvNamespaceScope(0, envs)).toBeNull()
  })

  it('选中环境映射为 namespace id 集合', () => {
    expect(resolveEnvNamespaceScope(2, envs)).toEqual([21, 22])
  })

  it('失效 env 或 env 选项未加载时保持停止作用域', () => {
    expect(resolveEnvNamespaceScope(999, envs)).toEqual([])
    expect(resolveEnvNamespaceScope(1, [])).toEqual([])
  })

  it('空映射保持空作用域', () => {
    expect(resolveEnvNamespaceScope(3, envs)).toEqual([])
  })

  it('失效 env 的名称作用域同样停止，避免字符串端点退回全量', () => {
    expect(resolveEnvNamespaceCodes(999, envs)).toEqual([])
    expect(resolveEnvNamespaceCodes(2, envs)).toEqual(['staging', 'dev'])
  })
})

describe('fetchPagedItemsByEnvScope', () => {
  it('多 namespace 分别携带 namespace 参数请求，不发无 scope 全量请求', async () => {
    const fetchPage = vi.fn((namespaceId: number | undefined) =>
      Promise.resolve({ items: [namespaceId], total: 1 }),
    )

    await expect(fetchPagedItemsByEnvScope([11, 12], fetchPage)).resolves.toEqual({
      items: [11, 12],
      total: 2,
      nextCursor: null,
    })
    expect(fetchPage).toHaveBeenCalledTimes(2)
    expect(fetchPage).toHaveBeenNthCalledWith(1, 11)
    expect(fetchPage).toHaveBeenNthCalledWith(2, 12)
  })

  it('空映射返回空结果且不请求全量数据', async () => {
    const fetchPage = vi.fn((namespaceId: number | undefined) =>
      Promise.resolve({ items: [namespaceId], total: 1 }),
    )

    await expect(fetchPagedItemsByEnvScope([], fetchPage)).resolves.toEqual({
      items: [],
      total: 0,
      nextCursor: null,
    })
    expect(fetchPage).not.toHaveBeenCalled()
  })

  it('全部环境仅发一次无 scope 请求', async () => {
    const fetchPage = vi.fn((namespaceId: number | undefined) =>
      Promise.resolve({ items: [namespaceId], total: 1 }),
    )

    await expect(fetchPagedItemsByEnvScope(null, fetchPage)).resolves.toEqual({
      items: [undefined],
      total: 1,
    })
    expect(fetchPage).toHaveBeenCalledWith(undefined)
  })

  it('多 namespace 分页先按命名空间拉足前缀，再统一排序裁剪', async () => {
    const fetchPage = vi.fn((namespaceId: number | undefined) =>
      Promise.resolve({
        items: namespaceId === 11 ? [9, 5] : [8, 4],
        total: 2,
      }),
    )

    await expect(
      fetchPagedItemsByEnvScope([11, 12], fetchPage, {
        page: 2,
        pageSize: 2,
        compare: (left, right) => right - left,
      }),
    ).resolves.toEqual({ items: [5, 4], total: 4, nextCursor: null })
    expect(fetchPage).toHaveBeenNthCalledWith(1, 11, { page: 1, pageSize: 4 })
    expect(fetchPage).toHaveBeenNthCalledWith(2, 12, { page: 1, pageSize: 4 })
  })
})

describe('resolveApiNamespaceId', () => {
  it('显式选中有效 namespace 时返回该 id', () => {
    expect(resolveApiNamespaceId(3, null)).toBe(3)
    expect(resolveApiNamespaceId(3, [1, 2, 3])).toBe(3)
  })

  it('未选中且 env 单 namespace 时返回该 id', () => {
    expect(resolveApiNamespaceId(null, [7])).toBe(7)
    expect(resolveApiNamespaceId(0, [7])).toBe(7)
  })

  it('多 namespace、空映射或失效选项返回停止信号，绝不回退全量', () => {
    expect(resolveApiNamespaceId(null, [1, 2])).toBeNull()
    expect(resolveApiNamespaceId(9, [1, 2])).toBeNull()
    expect(resolveApiNamespaceId(null, [])).toBeNull()
  })

  it('全部环境才允许不传 namespaceId', () => {
    expect(resolveApiNamespaceId(null, null)).toBeUndefined()
    expect(resolveApiNamespaceId(0, null)).toBeUndefined()
  })
})

// 观测范围（FR-213 页眉真源）→ 受限 namespace id 集合的映射（本次修复的核心纯逻辑）。
describe('resolveObservationScopeNamespaceIds', () => {
  const envs = [
    { id: 1, namespaces: [{ id: 11, name: 'prod' }] },
    { id: 2, namespaces: [{ id: 21, name: 'staging' }, { id: 22, name: 'dev' }] },
    { id: 3, namespaces: [] },
  ]

  it('范围失效 → 空集合（fail-closed，不回退全量）', () => {
    expect(resolveObservationScopeNamespaceIds({ kind: 'invalid', envId: 0, namespaceId: 0, empty: true, selection: { kind: 'invalid' } }, envs)).toEqual([])
  })

  it('全部环境且未选 namespace → null（不收窄）', () => {
    expect(resolveObservationScopeNamespaceIds({ kind: 'all', envId: 0, namespaceId: 0, empty: false, selection: { kind: 'all' } }, envs)).toBeNull()
  })

  it('全部环境下选了具体 namespace → 收窄到该单 namespace', () => {
    expect(resolveObservationScopeNamespaceIds({ kind: 'all', envId: 0, namespaceId: 3, empty: false, selection: { kind: 'all', namespaceId: 3 } }, envs)).toEqual([3])
  })

  it('选中环境 → 该 env 映射的 namespace 集合', () => {
    expect(resolveObservationScopeNamespaceIds({ kind: 'env', envId: 2, namespaceId: 0, empty: false, selection: { kind: 'env', envId: 2 } }, envs)).toEqual([21, 22])
  })

  it('选中环境下再选 namespace → 收窄到该单 namespace', () => {
    expect(resolveObservationScopeNamespaceIds({ kind: 'env', envId: 2, namespaceId: 21, empty: false, selection: { kind: 'env', envId: 2, namespaceId: 21 } }, envs)).toEqual([21])
  })

  it('选中空映射环境 → 空集合（停止请求）', () => {
    expect(resolveObservationScopeNamespaceIds({ kind: 'env', envId: 3, namespaceId: 0, empty: true, selection: { kind: 'env', envId: 3 } }, envs)).toEqual([])
  })
})
