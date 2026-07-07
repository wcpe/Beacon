// FR-159 场景化可变数据仓：每个域的数据集按当前场景惰性构建并缓存，
// 写操作直接改缓存内的可变对象（页面操作后列表状态真的变化）；
// 切换场景（或测试调用 resetMockData）即整体重建，回到该场景初始数据。

import { getMockScenario, subscribeMockScenario, type MockScenario } from './scenario'

type StoreBuilder<T> = (scenario: MockScenario) => T

const caches: { clear(): void }[] = []

/** 定义一个按场景缓存的可变数据仓，返回"取当前场景数据"的访问函数 */
export function defineScenarioStore<T>(build: StoreBuilder<T>): () => T {
  const cache = new Map<MockScenario, T>()
  caches.push(cache)
  return () => {
    const scenario = getMockScenario()
    const cached = cache.get(scenario)
    if (cached !== undefined) {
      return cached
    }
    const built = build(scenario)
    cache.set(scenario, built)
    return built
  }
}

/** 清空全部场景数据缓存（下次请求按场景重建）；测试与场景切换共用 */
export function resetMockData(): void {
  for (const cache of caches) {
    cache.clear()
  }
}

// 场景切换即重置：保证"切换场景或刷新页面时重置"的验收语义
subscribeMockScenario(() => {
  resetMockData()
})
