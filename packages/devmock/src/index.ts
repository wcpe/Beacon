// @beacon/devmock 入口（FR-159 全域 mock 数据层）：
// 聚合各域 handlers 供浏览器（msw/browser worker）与 Node 测试端（msw/node setupServer）共用；
// 四态场景（empty/normal/huge/error）由 scenario.ts 提供，数据仓按场景惰性构建、切换即重置（store.ts）。

import { http, HttpResponse, type HttpHandler } from 'msw'
import { identityHandlers } from './domains/identity'
import { namespaceHandlers } from './domains/namespace'
import { zoneAuthorityHandlers } from './domains/zone-authority'
import { metricsHealthHandlers } from './domains/metrics-health'
import { connectionsMessagesHandlers } from './domains/connections-messages'
import { archiveHandlers } from './domains/archive'
import { configCenterHandlers } from './domains/config-center'
import { fileAssetsHandlers } from './domains/file-assets'
import { deliveryHandlers } from './domains/delivery'
import { systemHandlers } from './domains/system'
import { observabilityHandlers } from './domains/observability'
import { fallbackHandlers } from './http'

export {
  MOCK_SCENARIOS,
  getMockScenario,
  setMockScenario,
  subscribeMockScenario,
} from './scenario'
export type { MockScenario } from './scenario'
export { resetMockData } from './store'
export { MOCK_TRACE_ID, errorBody } from './http'
export type { MockErrorBody, Paged } from './http'
export { BASE_MS } from './support'

// 各域响应类型全部导出，供页面 agent 复用
export * from './data/cluster'
export * from './domains/identity'
export * from './domains/namespace'
export * from './domains/zone-authority'
export * from './domains/metrics-health'
export * from './domains/connections-messages'
export * from './domains/archive'
export * from './domains/config-center'
export * from './domains/file-assets'
export * from './domains/delivery'
export * from './domains/system'
export * from './domains/observability'

export interface ControlPlaneStatusFixture {
  phase: string
  release: string
  web: string
}

export const controlPlaneStatusFixture: ControlPlaneStatusFixture = {
  phase: '0.21.x',
  release: 'P1',
  web: 'apps/web',
}

export const controlPlaneStatusPath = '/admin/v1/control-plane/status'

export const controlPlaneHandlers: HttpHandler[] = [
  http.get(controlPlaneStatusPath, () => HttpResponse.json(controlPlaneStatusFixture)),
]

/** 按域分组的 handlers（页面 agent 可按需取用） */
export const domainHandlers = {
  controlPlane: controlPlaneHandlers,
  identity: identityHandlers,
  namespace: namespaceHandlers,
  zoneAuthority: zoneAuthorityHandlers,
  metricsHealth: metricsHealthHandlers,
  connectionsMessages: connectionsMessagesHandlers,
  archive: archiveHandlers,
  configCenter: configCenterHandlers,
  fileAssets: fileAssetsHandlers,
  delivery: deliveryHandlers,
  system: systemHandlers,
  observability: observabilityHandlers,
} satisfies Record<string, HttpHandler[]>

/** 全量 handlers：浏览器 worker 与 Node setupServer 共用。
    fallbackHandlers 兜底置于末尾——真实 handler 优先命中，未匹配的 /admin|/beacon 才回 JSON 404。 */
export const allHandlers: HttpHandler[] = [...Object.values(domainHandlers).flat(), ...fallbackHandlers]

let controlPlaneWorkerStart: Promise<void> | undefined

/** 浏览器端启动 mock（注册全量 handlers；Node 端为空操作，测试请用 msw/node setupServer） */
export function startControlPlaneMocking(): Promise<void> {
  if (!('window' in globalThis)) {
    return Promise.resolve()
  }

  if (controlPlaneWorkerStart) {
    return controlPlaneWorkerStart
  }

  controlPlaneWorkerStart = import('msw/browser').then(async ({ setupWorker }) => {
    await setupWorker(...allHandlers).start({
      onUnhandledRequest: 'bypass',
      serviceWorker: {
        url: '/mockServiceWorker.js',
      },
    })
  })

  return controlPlaneWorkerStart
}
