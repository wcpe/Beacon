// mock 场景四态基建（FR-159）：空态 / 常规 / 超大量 / 异常。
// 浏览器端可运行时切换并持久化（localStorage + URL 参数），Node 测试端默认常规态；
// 各域 handlers 在每次请求时读取当前场景，页面无需感知切换细节。

export type MockScenario = 'empty' | 'normal' | 'huge' | 'error'

export const MOCK_SCENARIOS: readonly MockScenario[] = ['empty', 'normal', 'huge', 'error']

/** 数据量档位：baseline 保留各域既有样例规模，其余档位以目标条目数扩展。 */
export const MOCK_DATA_VOLUMES = [
  { value: 'baseline', itemCount: null },
  { value: 'small', itemCount: 10 },
  { value: 'normal', itemCount: 50 },
  { value: 'large', itemCount: 500 },
  { value: 'xlarge', itemCount: 5000 },
] as const

export type MockDataVolume = (typeof MOCK_DATA_VOLUMES)[number]['value']

/** 接口响应延时档位，单位为毫秒。 */
export const MOCK_LATENCIES = [
  { value: 'none', milliseconds: 0 },
  { value: 'fast', milliseconds: 200 },
  { value: 'slow', milliseconds: 800 },
  { value: 'verySlow', milliseconds: 2500 },
] as const

export type MockLatency = (typeof MOCK_LATENCIES)[number]['value']

const STORAGE_KEY = 'beacon-mock-scenario'
const QUERY_KEY = 'mockScenario'
const DATA_VOLUME_STORAGE_KEY = 'beacon-mock-data-volume'
const DATA_VOLUME_QUERY_KEY = 'mockDataVolume'
const LATENCY_STORAGE_KEY = 'beacon-mock-latency'
const LATENCY_QUERY_KEY = 'mockLatency'
const DEFAULT_DATA_VOLUME: MockDataVolume = 'baseline'
const DEFAULT_LATENCY: MockLatency = 'none'

type ScenarioListener = (scenario: MockScenario) => void

function isMockScenario(value: unknown): value is MockScenario {
  return typeof value === 'string' && (MOCK_SCENARIOS as readonly string[]).includes(value)
}

function readInitialScenario(): MockScenario {
  if (!('window' in globalThis)) {
    return 'normal'
  }
  try {
    const fromQuery = new URLSearchParams(window.location.search).get(QUERY_KEY)
    if (isMockScenario(fromQuery)) {
      return fromQuery
    }
    const fromStorage = window.localStorage.getItem(STORAGE_KEY)
    if (isMockScenario(fromStorage)) {
      return fromStorage
    }
  } catch {
    // 隐私模式等环境读取 localStorage 失败时回落默认场景
  }
  return 'normal'
}

let current: MockScenario = readInitialScenario()
const listeners = new Set<ScenarioListener>()

export function getMockScenario(): MockScenario {
  return current
}

export function setMockScenario(scenario: MockScenario): void {
  if (scenario === current) {
    return
  }
  current = scenario
  if ('window' in globalThis) {
    try {
      window.localStorage.setItem(STORAGE_KEY, scenario)
    } catch {
      // 持久化失败不影响本次会话内切换
    }
  }
  for (const listener of listeners) {
    listener(scenario)
  }
}

export function subscribeMockScenario(listener: ScenarioListener): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

type DataVolumeListener = (volume: MockDataVolume) => void
type LatencyListener = (latency: MockLatency) => void

function isMockDataVolume(value: unknown): value is MockDataVolume {
  return typeof value === 'string' && MOCK_DATA_VOLUMES.some((option) => option.value === value)
}

function isMockLatency(value: unknown): value is MockLatency {
  return typeof value === 'string' && MOCK_LATENCIES.some((option) => option.value === value)
}

function readInitialValue<T extends string>(
  queryKey: string,
  storageKey: string,
  isValue: (value: unknown) => value is T,
  fallback: T,
): T {
  if (!('window' in globalThis)) {
    return fallback
  }
  try {
    const fromQuery = new URLSearchParams(window.location.search).get(queryKey)
    if (isValue(fromQuery)) {
      return fromQuery
    }
    const fromStorage = window.localStorage.getItem(storageKey)
    if (isValue(fromStorage)) {
      return fromStorage
    }
  } catch {
    // 隐私模式等环境读取 localStorage 失败时回落默认值
  }
  return fallback
}

let currentDataVolume = readInitialValue(
  DATA_VOLUME_QUERY_KEY,
  DATA_VOLUME_STORAGE_KEY,
  isMockDataVolume,
  DEFAULT_DATA_VOLUME,
)
let currentLatency = readInitialValue(
  LATENCY_QUERY_KEY,
  LATENCY_STORAGE_KEY,
  isMockLatency,
  DEFAULT_LATENCY,
)
const dataVolumeListeners = new Set<DataVolumeListener>()
const latencyListeners = new Set<LatencyListener>()

export function getMockDataVolume(): MockDataVolume {
  return currentDataVolume
}

export function getMockDataVolumeCount(): number {
  return MOCK_DATA_VOLUMES.find((option) => option.value === currentDataVolume)?.itemCount ?? 0
}

export function setMockDataVolume(volume: MockDataVolume): void {
  if (volume === currentDataVolume) {
    return
  }
  currentDataVolume = volume
  if ('window' in globalThis) {
    try {
      window.localStorage.setItem(DATA_VOLUME_STORAGE_KEY, volume)
    } catch {
      // 持久化失败不影响本次会话内切换
    }
  }
  for (const listener of dataVolumeListeners) {
    listener(volume)
  }
}

export function subscribeMockDataVolume(listener: DataVolumeListener): () => void {
  dataVolumeListeners.add(listener)
  return () => {
    dataVolumeListeners.delete(listener)
  }
}

export function getMockLatency(): MockLatency {
  return currentLatency
}

export function getMockLatencyMilliseconds(): number {
  return MOCK_LATENCIES.find((option) => option.value === currentLatency)?.milliseconds ?? 0
}

export function setMockLatency(latency: MockLatency): void {
  if (latency === currentLatency) {
    return
  }
  currentLatency = latency
  if ('window' in globalThis) {
    try {
      window.localStorage.setItem(LATENCY_STORAGE_KEY, latency)
    } catch {
      // 持久化失败不影响本次会话内切换
    }
  }
  for (const listener of latencyListeners) {
    listener(latency)
  }
}

export function subscribeMockLatency(listener: LatencyListener): () => void {
  latencyListeners.add(listener)
  return () => {
    latencyListeners.delete(listener)
  }
}
