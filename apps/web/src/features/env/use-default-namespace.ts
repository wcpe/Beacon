// 交付域默认命名空间补全：页眉观测范围停在「全部命名空间」时，自动选中第一个可用命名空间，
// 让交付域四页（文件资产 / 配置中心 / 变更单 / 交付历史）一进页面就能用，不必先被
// 「请先在页眉左侧选择命名空间」的阻塞引导挡住。
//
// 约定与边界：
// - **只由交付域四页调用**；页眉（shell/env-filter）与可观测域的默认行为不变——可观测域的
//   「全部命名空间」往往就是它想要的默认，不该被交付域的需求带偏。
// - 已有具体命名空间（namespaceId > 0）时一律不动，绝不覆盖用户的选择。
// - 只写全局观测范围（setObservationScope）：页眉选择器读同一份快照，因此页眉显示与页面取舍
//   天然一致，不会出现「页眉显示全部、页面却按第一个命名空间取数」的错位。
// - 范围失效（invalid：env / namespace 已被删除）不在此处自动改写，交回页眉的冲突处置，
//   避免把「需要人确认的范围」静默替换成别的命名空间。
// - 交付域不以「全部命名空间」为写目标：用户在交付页把页眉切回「全部命名空间」时，会被立刻补回
//   第一个可用命名空间（页眉同步显示该命名空间，仍是同一份真相，不会出现显示与取数错位）。
import { useEffect } from 'react'

import type { EnvItem } from '@beacon/contracts'

import {
  deriveObservationScope,
  type ObservationScopeSelection,
  selectObservationNamespace,
  setObservationScope,
  useObservationScopeSelection,
} from './observation-scope'
import { useEnvOptions, useEnvOptionsQuery } from './use-env-scope'

/** 默认命名空间解析结果：页面据此区分「还在解析」与「确实没有可用命名空间」。 */
export interface DefaultObservationNamespace {
  /** 最终生效的命名空间 id；0 表示尚无具体命名空间（解析中，或确实没有可用命名空间）。 */
  namespaceId: number
  /** 仍在解析：env 选项加载中，或已选出候选但默认值尚未回流到快照。此时应给加载态而非引导。 */
  pending: boolean
}

/**
 * 可选的默认命名空间候选。
 *
 * 选中了具体 env 时只在该 env 内挑——跨 env 写入会让派生范围判定为 invalid（namespace 不属于该 env）；
 * 未选具体 env（全部环境）时按 env 顺序摊平取第一个。EnvItem.namespaces 只带 id / name，
 * 没有「是否有服」的字段，故不额外做有服优先。
 */
function resolveCandidates(
  selection: ObservationScopeSelection,
  envs: readonly Pick<EnvItem, 'id' | 'namespaces'>[],
): readonly { id: number }[] {
  if (selection.kind === 'env') {
    return envs.find((env) => env.id === selection.envId)?.namespaces ?? []
  }
  if (selection.kind === 'all') {
    return envs.flatMap((env) => env.namespaces)
  }
  return []
}

/**
 * 交付域默认命名空间：作用域尚未选定具体命名空间时，自动选中第一个可用命名空间。
 *
 * 写入放在 effect 里（不在 render 期间改全局状态），且只在 namespaceId === 0 时写；
 * 写入后 namespaceId > 0，依赖变化即不再命中条件，因此不会无限循环。
 * 首帧仍可能是 0（写入落后一帧），调用方应按 pending 显示加载态。
 */
export function useDefaultObservationNamespaceState(): DefaultObservationNamespace {
  const selection = useObservationScopeSelection()
  const envs = useEnvOptions()
  const query = useEnvOptionsQuery()
  const namespaceId = deriveObservationScope(selection, envs).namespaceId

  useEffect(() => {
    // 已有具体命名空间：保持用户选择；失效范围：交回页眉处置。
    if (namespaceId > 0 || selection.kind === 'invalid') {
      return
    }
    const candidates = resolveCandidates(selection, envs)
    if (candidates.length === 0) {
      return
    }
    setObservationScope(selectObservationNamespace(selection, candidates[0].id))
  }, [namespaceId, selection, envs])

  // 解析中不应闪出「请先选命名空间」的引导：env 选项还在加载，或候选已就绪但默认值尚未回流。
  const pending =
    namespaceId === 0 &&
    selection.kind !== 'invalid' &&
    (query.isLoading || resolveCandidates(selection, envs).length > 0)

  return { namespaceId, pending }
}

/** 只要生效命名空间 id 时的简写（0 的语义见 DefaultObservationNamespace）。 */
export function useDefaultObservationNamespace(): number {
  return useDefaultObservationNamespaceState().namespaceId
}
