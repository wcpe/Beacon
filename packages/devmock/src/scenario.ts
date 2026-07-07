// mock 场景四态基建（FR-159）：空态 / 常规 / 超大量 / 异常。
// 浏览器端可运行时切换并持久化（localStorage + URL 参数），Node 测试端默认常规态；
// 各域 handlers 在每次请求时读取当前场景，页面无需感知切换细节。

export type MockScenario = 'empty' | 'normal' | 'huge' | 'error'

export const MOCK_SCENARIOS: readonly MockScenario[] = ['empty', 'normal', 'huge', 'error']

const STORAGE_KEY = 'beacon-mock-scenario'
const QUERY_KEY = 'mockScenario'

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
