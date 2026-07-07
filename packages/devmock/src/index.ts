import { http, HttpResponse, type HttpHandler } from 'msw'

export {
  MOCK_SCENARIOS,
  getMockScenario,
  setMockScenario,
  subscribeMockScenario,
} from './scenario'
export type { MockScenario } from './scenario'

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

let controlPlaneWorkerStart: Promise<void> | undefined

export function startControlPlaneMocking(): Promise<void> {
  if (!('window' in globalThis)) {
    return Promise.resolve()
  }

  if (controlPlaneWorkerStart) {
    return controlPlaneWorkerStart
  }

  controlPlaneWorkerStart = import('msw/browser').then(async ({ setupWorker }) => {
    await setupWorker(...controlPlaneHandlers).start({
      onUnhandledRequest: 'bypass',
      serviceWorker: {
        url: '/mockServiceWorker.js',
      },
    })
  })

  return controlPlaneWorkerStart
}
