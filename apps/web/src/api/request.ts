// 交付大域共用的底层请求封装：与集群域 cluster.ts 同构，但复用其 ApiClientError 类，
// 保证 instanceof 判定一致。非 2xx 抛脱敏 message（ADR-0057），204 返回 undefined。

import { ApiClientError } from './cluster'

interface ErrorBodyShape {
  code?: unknown
  message?: unknown
}

/** 统一请求封装：非 2xx 抛 ApiClientError（取脱敏 message），空响应体返回 undefined。 */
export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await response.text()
  const parsed: unknown = text === '' ? null : JSON.parse(text)
  if (!response.ok) {
    const shape = (parsed ?? {}) as ErrorBodyShape
    const code = typeof shape.code === 'string' ? shape.code : 'unknown'
    const message =
      typeof shape.message === 'string' ? shape.message : `请求失败（HTTP ${String(response.status)}）`
    throw new ApiClientError(response.status, code, message)
  }
  return parsed as T
}

/** 拼装 query 串：跳过 null / undefined / 空串。 */
export function buildQuery(
  params: Record<string, string | number | boolean | null | undefined>,
): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === null || value === undefined || value === '') {
      continue
    }
    search.set(key, String(value))
  }
  const query = search.toString()
  return query === '' ? '' : `?${query}`
}
