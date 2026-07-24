// 页眉刷新路径 → queryKey 映射单测（FR-196）
import { describe, expect, it } from 'vitest'

import { queryKeysForPath } from '../../shell/page-refresh-button'

describe('queryKeysForPath', () => {
  it('dashboard 失效 dashboard + shell metrics', () => {
    expect(queryKeysForPath('/dashboard')).toEqual([['dashboard'], ['shell', 'metrics']])
  })

  it('servers / identity-conflicts', () => {
    expect(queryKeysForPath('/servers')).toEqual([['servers'], ['identities'], ['health']])
    expect(queryKeysForPath('/identity-conflicts')).toEqual([['servers'], ['identities'], ['health']])
  })

  it('alert-events 含通知与指标', () => {
    expect(queryKeysForPath('/alert-events')).toEqual([
      ['alert-events'],
      ['dashboard', 'alerts'],
      ['shell', 'notifications'],
      ['shell', 'metrics'],
    ])
  })

  it('未知路径返回空（调用方走全量 invalidate）', () => {
    expect(queryKeysForPath('/license')).toEqual([])
  })
})
