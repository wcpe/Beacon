// 控制面在线更新检查 hook（FR-100，消费 FR-99 端点）。
//
// 与 SystemHeader 的 5s systemStatus 轮询解耦：那是控制面健康高频心跳；更新检查是低频拉
// GitHub release（叠后端服务端缓存），高频会打爆 GitHub 限流。故独立 useQuery(['update-check'])，
// refetchInterval 与 store 的 update.check-interval-hours 对齐（默认 6h）；update.auto-check-enabled
// 关闭时禁用自动轮询、仅手动「立即检查」。
//
// 设置值复用既有 ['settings'] 查询（与运维设置页同 key，react-query 去重、不多发请求）；设置未就绪
// 时按默认（开 + 6h）兜底，待设置到达后周期自然对齐。

import { useQuery, useQueryClient } from '@tanstack/react-query'
import { checkUpdate, listSettings } from '@/api/client'
import type { SettingView, UpdateCheckView } from '@/api/types'

// 自动检查周期默认值（小时）：与后端 update.check-interval-hours 默认一致，设置未就绪时兜底。
const DEFAULT_INTERVAL_HOURS = 6
// 周期下界（小时）：与后端白名单 [1,168] 下界一致，防异常小值导致过频轮询。
const MIN_INTERVAL_HOURS = 1
const HOUR_MS = 60 * 60 * 1000

// 从设置项列表取某 key 的当前值；缺项返回 undefined。
function settingValue(settings: SettingView[] | undefined, key: string): string | undefined {
  return settings?.find((s) => s.key === key)?.value
}

// 由设置派生自动检查开关（默认开）。
export function deriveAutoCheckEnabled(settings: SettingView[] | undefined): boolean {
  const v = settingValue(settings, 'update.auto-check-enabled')
  // 缺项 / 非法值按默认开
  return v === undefined ? true : v !== 'false'
}

// 由设置派生轮询周期（毫秒）；非法 / 越界回退默认，且不低于下界。
export function deriveIntervalMs(settings: SettingView[] | undefined): number {
  const raw = settingValue(settings, 'update.check-interval-hours')
  const parsed = raw === undefined ? NaN : Number(raw)
  const hours = Number.isFinite(parsed) && parsed >= MIN_INTERVAL_HOURS ? parsed : DEFAULT_INTERVAL_HOURS
  return hours * HOUR_MS
}

export interface UpdateCheckState {
  data: UpdateCheckView | undefined
  isLoading: boolean
  isError: boolean
  // 手动「立即检查」：绕服务端缓存强制刷新（?force=true）后回填同一查询缓存
  refresh: () => Promise<unknown>
}

// 订阅更新检查查询：低频自动轮询（周期 / 开关由 store 设置决定）+ 暴露手动强制刷新。
export function useUpdateCheck(): UpdateCheckState {
  const queryClient = useQueryClient()
  // 复用运维设置页的 ['settings'] 查询取自动检查开关 / 周期（同 key 去重）。
  const { data: settings } = useQuery({ queryKey: ['settings'], queryFn: listSettings })
  const autoEnabled = deriveAutoCheckEnabled(settings)
  const intervalMs = deriveIntervalMs(settings)

  const query = useQuery({
    queryKey: ['update-check'],
    // 自动 / 首屏走非强制：命中服务端缓存即不打 GitHub
    queryFn: () => checkUpdate(false),
    // 自动检查关闭时禁用周期轮询；仅手动触发
    refetchInterval: autoEnabled ? intervalMs : false,
    // 窗口聚焦不额外打 GitHub（低频策略）；周期到点才拉
    refetchOnWindowFocus: false,
  })

  return {
    data: query.data,
    isLoading: query.isLoading,
    isError: query.isError,
    // 手动「立即检查」：force=true 绕服务端缓存强制刷新，结果回填同一 ['update-check'] 缓存。
    // 用 fetchQuery（而非 refetch）以便本次单独走 force 分支，不改自动轮询的非强制 queryFn。
    refresh: () =>
      queryClient.fetchQuery({
        queryKey: ['update-check'],
        queryFn: () => checkUpdate(true),
      }),
  }
}
