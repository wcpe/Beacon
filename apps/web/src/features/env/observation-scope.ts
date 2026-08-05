import { useSyncExternalStore } from 'react'

import type { EnvItem } from '@beacon/contracts'

/** 全部环境 / 全部 namespace 的显式哨兵，只用于页眉观测选择。 */
export const ALL_OBSERVATION_ENV = 0
export const ALL_OBSERVATION_NAMESPACE = 0

const STORAGE_KEY = 'beacon.observationScope.v1'

export type ObservationScopeSelection =
  | { kind: 'all'; namespaceId?: number }
  | { kind: 'env'; envId: number; namespaceId?: number }
  | { kind: 'invalid'; savedEnvId?: number; savedNamespaceId?: number }

export interface DerivedObservationScope {
  kind: ObservationScopeSelection['kind']
  envId: number
  namespaceId: number
  empty: boolean
  selection: ObservationScopeSelection
}

const listeners = new Set<() => void>()
let snapshot = readSelection()

function readSelection(): ObservationScopeSelection {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw === null) return { kind: 'all' }
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) return { kind: 'invalid' }
    const value = parsed as ObservationScopeSelection
    if (value.kind === 'all' || value.kind === 'env') return value
  } catch {
    return { kind: 'invalid' }
  }
  return { kind: 'invalid' }
}

function publish(next: ObservationScopeSelection): void {
  snapshot = next
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
  } catch {
    // 本次会话继续使用内存快照；不能把存储失败伪装成全部范围。
  }
  listeners.forEach((listener) => { listener() })
}

export function setObservationScope(next: ObservationScopeSelection): void {
  publish(next)
}

export function selectObservationEnv(_current: ObservationScopeSelection, envId: number): ObservationScopeSelection {
  if (envId === ALL_OBSERVATION_ENV) return { kind: 'all' }
  return { kind: 'env', envId }
}

export function selectObservationNamespace(selection: ObservationScopeSelection, namespaceId: number): ObservationScopeSelection {
  if (selection.kind === 'all') return namespaceId === ALL_OBSERVATION_NAMESPACE ? { kind: 'all' } : { kind: 'all', namespaceId }
  if (selection.kind === 'env') return namespaceId === ALL_OBSERVATION_NAMESPACE ? { kind: 'env', envId: selection.envId } : { kind: 'env', envId: selection.envId, namespaceId }
  return selection
}

export function deriveObservationScope(selection: ObservationScopeSelection, envs: readonly Pick<EnvItem, 'id' | 'namespaces'>[]): DerivedObservationScope {
  if (selection.kind === 'invalid') return { kind: 'invalid', envId: 0, namespaceId: 0, empty: true, selection }
  const allNamespaces = envs.flatMap((env) => env.namespaces)
  if (selection.kind === 'all') {
    const namespaceId = selection.namespaceId ?? ALL_OBSERVATION_NAMESPACE
    const valid = namespaceId === ALL_OBSERVATION_NAMESPACE || allNamespaces.some((namespace) => namespace.id === namespaceId)
    return valid
      ? { kind: 'all', envId: 0, namespaceId, empty: false, selection }
      : { kind: 'invalid', envId: 0, namespaceId: 0, empty: true, selection: { kind: 'invalid', savedNamespaceId: namespaceId } }
  }
  const env = envs.find((item) => item.id === selection.envId)
  if (env === undefined) return { kind: 'invalid', envId: 0, namespaceId: 0, empty: true, selection: { kind: 'invalid', savedEnvId: selection.envId, savedNamespaceId: selection.namespaceId } }
  const namespaceId = selection.namespaceId ?? ALL_OBSERVATION_NAMESPACE
  const valid = namespaceId === ALL_OBSERVATION_NAMESPACE || env.namespaces.some((namespace) => namespace.id === namespaceId)
  if (!valid) return { kind: 'invalid', envId: 0, namespaceId: 0, empty: true, selection: { kind: 'invalid', savedEnvId: selection.envId, savedNamespaceId: namespaceId } }
  return { kind: 'env', envId: env.id, namespaceId, empty: env.namespaces.length === 0, selection }
}

export function useObservationScopeSelection(): ObservationScopeSelection {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    () => snapshot,
    () => snapshot,
  )
}
