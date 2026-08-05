import { describe, expect, it } from 'vitest'

import {
  ALL_OBSERVATION_ENV,
  ALL_OBSERVATION_NAMESPACE,
  deriveObservationScope,
  selectObservationEnv,
} from '../../features/env/observation-scope'

const envs = [
  { id: 1, name: '线上', namespaces: [{ id: 11, name: 'prod' }] },
  { id: 2, name: '灰度', namespaces: [{ id: 21, name: 'staging' }, { id: 22, name: 'canary' }] },
  { id: 3, name: '空环境', namespaces: [] },
]

describe('FR-214 observation scope selection', () => {
  it('切换 env 必须原子重置 namespace 为该环境全部', () => {
    expect(selectObservationEnv({ kind: 'env', envId: 1, namespaceId: 11 }, 2)).toEqual({ kind: 'env', envId: 2 })
  })

  it('支持全部、env 全部、env 内单 namespace 与空映射四态', () => {
    expect(deriveObservationScope({ kind: 'all' }, envs)).toMatchObject({ kind: 'all', envId: ALL_OBSERVATION_ENV, namespaceId: ALL_OBSERVATION_NAMESPACE })
    expect(deriveObservationScope({ kind: 'env', envId: 2 }, envs)).toMatchObject({ kind: 'env', envId: 2, namespaceId: ALL_OBSERVATION_NAMESPACE, empty: false })
    expect(deriveObservationScope({ kind: 'env', envId: 2, namespaceId: 21 }, envs)).toMatchObject({ kind: 'env', envId: 2, namespaceId: 21, empty: false })
    expect(deriveObservationScope({ kind: 'env', envId: 3 }, envs)).toMatchObject({ kind: 'env', envId: 3, empty: true })
  })

  it('失效 env 或错配 namespace 必须进入 invalid，绝不回退全部', () => {
    expect(deriveObservationScope({ kind: 'env', envId: 99 }, envs).kind).toBe('invalid')
    expect(deriveObservationScope({ kind: 'env', envId: 1, namespaceId: 21 }, envs).kind).toBe('invalid')
  })
})
