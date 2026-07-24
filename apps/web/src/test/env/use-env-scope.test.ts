// env 作用域纯函数单测（FR-178）：不过滤 / 单 ns / 多 ns / 空映射 的列表收窄与 API namespaceId 合成。
import { describe, expect, it } from 'vitest'

import {
  filterItemsByEnvCodes,
  filterItemsByEnvScope,
  needsClientEnvFilter,
  resolveApiNamespaceId,
} from '../../features/env/use-env-scope'

describe('filterItemsByEnvScope', () => {
  const items = [
    { id: 'a', namespaceId: 1 },
    { id: 'b', namespaceId: 2 },
    { id: 'c', namespaceId: 3 },
  ]

  it('scope=null（全部环境）原样返回', () => {
    expect(filterItemsByEnvScope(items, null)).toEqual(items)
  })

  it('空映射返回空数组', () => {
    expect(filterItemsByEnvScope(items, [])).toEqual([])
  })

  it('只保留落在 scope 内的行', () => {
    expect(filterItemsByEnvScope(items, [1, 3]).map((i) => i.id)).toEqual(['a', 'c'])
  })
})

describe('filterItemsByEnvCodes', () => {
  const items = [
    { id: 1, namespace: 'prod' },
    { id: 2, namespace: 'staging' },
    { id: 3, namespace: 'dev' },
  ]

  it('codes=null 原样返回', () => {
    expect(filterItemsByEnvCodes(items, null)).toEqual(items)
  })

  it('空 codes 返回空数组', () => {
    expect(filterItemsByEnvCodes(items, [])).toEqual([])
  })

  it('只保留 code 命中的行', () => {
    expect(filterItemsByEnvCodes(items, ['prod', 'dev']).map((i) => i.id)).toEqual([1, 3])
  })
})

describe('resolveApiNamespaceId', () => {
  it('显式选中 >0 时优先返回该 id', () => {
    expect(resolveApiNamespaceId(3, null)).toBe(3)
    expect(resolveApiNamespaceId(3, [1, 2, 3])).toBe(3)
  })

  it('选中不在 env 范围内且 env 单 ns 时回退到该 ns', () => {
    expect(resolveApiNamespaceId(9, [5])).toBe(5)
  })

  it('选中不在 env 范围内且 env 多 ns 时返回 undefined', () => {
    expect(resolveApiNamespaceId(9, [1, 2])).toBeUndefined()
  })

  it('未选择且 env 单 ns 时返回该 ns', () => {
    expect(resolveApiNamespaceId(null, [7])).toBe(7)
    expect(resolveApiNamespaceId(0, [7])).toBe(7)
  })

  it('未选择且全部环境 / 多 ns 时不传 namespaceId', () => {
    expect(resolveApiNamespaceId(null, null)).toBeUndefined()
    expect(resolveApiNamespaceId(0, null)).toBeUndefined()
    expect(resolveApiNamespaceId(null, [1, 2])).toBeUndefined()
  })
})

describe('needsClientEnvFilter', () => {
  it('全部环境不需要客户端二次过滤', () => {
    expect(needsClientEnvFilter(null)).toBe(false)
  })

  it('单 ns 不需要（可直接传 API）', () => {
    expect(needsClientEnvFilter([1])).toBe(false)
  })

  it('多 ns 或空映射需要客户端过滤', () => {
    expect(needsClientEnvFilter([1, 2])).toBe(true)
    expect(needsClientEnvFilter([])).toBe(true)
  })
})
