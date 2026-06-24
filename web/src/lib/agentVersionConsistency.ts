// agent 版本一致性纯逻辑层（FR-86，见 ADR-0039）：按环境（namespace）聚合在册实例的 agent 构建版本，
// 算出每环境的「多数版本」，并判定某实例的 agentVersion 是否与其环境多数版本不一致。
// 组件层只管渲染黄标，判定逻辑全在这里——无副作用、可穷举单测。

import type { InstanceView } from '@/api/types'

// 按环境聚合的多数版本表：namespace → 该环境出现次数最多的非空 agentVersion（无非空版本则该环境无条目）。
export type MajorityVersionByNamespace = Map<string, string>

// buildMajorityVersions 计算每个环境的 agent 多数版本。
// 仅统计非空 agentVersion（空表示旧 agent 未上报、不参与众数）；并列时取版本字符串字典序最小者（确定性，避免抖动）。
export function buildMajorityVersions(instances: InstanceView[]): MajorityVersionByNamespace {
  // namespace → (version → 计数)
  const counts = new Map<string, Map<string, number>>()
  for (const i of instances) {
    const v = i.agentVersion
    if (!v) continue // 空版本不参与统计
    let byVer = counts.get(i.namespace)
    if (!byVer) {
      byVer = new Map<string, number>()
      counts.set(i.namespace, byVer)
    }
    byVer.set(v, (byVer.get(v) ?? 0) + 1)
  }

  const majority: MajorityVersionByNamespace = new Map()
  for (const [ns, byVer] of counts) {
    let best = ''
    let bestCount = -1
    for (const [ver, count] of byVer) {
      // 取计数最大者；并列时取字典序较小的版本，保证结果稳定可测。
      if (count > bestCount || (count === bestCount && ver < best)) {
        best = ver
        bestCount = count
      }
    }
    majority.set(ns, best)
  }
  return majority
}

// isAgentVersionMismatch 判定某实例 agent 版本是否与其环境多数版本不一致。
// 空版本（旧 agent 未上报）不算不一致（显「未知」而非黄标）；多数表无该环境条目（全环境皆空）亦不算。
export function isAgentVersionMismatch(
  instance: Pick<InstanceView, 'namespace' | 'agentVersion'>,
  majority: MajorityVersionByNamespace,
): boolean {
  const v = instance.agentVersion
  if (!v) return false
  const maj = majority.get(instance.namespace)
  if (!maj) return false
  return v !== maj
}
