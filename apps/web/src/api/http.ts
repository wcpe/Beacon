// 可观测域数据获取共用请求封装：与 cluster.ts 同口径（非 2xx 抛脱敏 message、204 返回 undefined）。
// cluster.ts 的 request 未导出，故这里为可观测四页新建一份等价实现；ApiClientError 复用 cluster.ts 导出。

import { ApiClientError, parseApiJson } from './cluster'

export { ApiClientError, buildQuery } from './cluster'

interface ErrorBodyShape {
  code?: unknown
  message?: unknown
}

/** 统一请求封装：非 2xx 抛 ApiClientError（取脱敏 message），204 返回 undefined。 */
export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await response.text()
  const parsed: unknown = parseApiJson(text, response.status)
  if (!response.ok) {
    const shape = (parsed ?? {}) as ErrorBodyShape
    const code = typeof shape.code === 'string' ? shape.code : 'unknown'
    const message =
      typeof shape.message === 'string' ? shape.message : `请求失败（HTTP ${String(response.status)}）`
    throw new ApiClientError(response.status, code, message)
  }
  return parsed as T
}
