import { controlPlaneStatusPath, type ControlPlaneStatusFixture } from '@beacon/devmock'

export interface ControlPlaneStatus {
  phase: string
  release: string
  web: string
}

export async function fetchControlPlaneStatus(): Promise<ControlPlaneStatus> {
  const response = await fetch(controlPlaneStatusPath)
  if (!response.ok) {
    throw new Error(`控制面状态请求失败：${String(response.status)}`)
  }

  const payload: unknown = await response.json()
  if (!isControlPlaneStatusFixture(payload)) {
    throw new Error('控制面状态响应格式无效')
  }

  return toControlPlaneStatus(payload)
}

function toControlPlaneStatus(fixture: ControlPlaneStatusFixture): ControlPlaneStatus {
  return {
    phase: fixture.phase,
    release: fixture.release,
    web: fixture.web,
  }
}

function isControlPlaneStatusFixture(value: unknown): value is ControlPlaneStatusFixture {
  if (typeof value !== 'object' || value === null) {
    return false
  }

  const candidate = value as Record<string, unknown>
  return (
    typeof candidate.phase === 'string' &&
    typeof candidate.release === 'string' &&
    typeof candidate.web === 'string'
  )
}
