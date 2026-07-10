// FR-159 mock handler 基建：统一错误体、error 场景注入、分页与请求解析工具。
// 契约依据 docs/API.md「第二版契约草案」通用约定：统一错误体 {code,message,traceId}、
// 分页 page/pageSize（+keyword）响应 {items,total}。

import { http, HttpResponse, type HttpHandler } from 'msw'
import type { MockErrorBody, Paged } from '@beacon/contracts'
import { getMockScenario } from './scenario'

/** mock 固定 traceId（确定性，测试可断言） */
export const MOCK_TRACE_ID = 'trace-devmock'

/** 构造统一错误体 */
export function errorBody(code: string, message: string): MockErrorBody {
  return { code, message, traceId: MOCK_TRACE_ID }
}

/** 构造统一错误响应（可附加额外字段，如 sensitive 标记） */
export function jsonError(
  status: number,
  code: string,
  message: string,
  extra?: Record<string, unknown>,
): Response {
  return HttpResponse.json({ ...errorBody(code, message), ...extra }, { status })
}

/** error 场景注入的 500 错误 */
export function internalError(): Response {
  return jsonError(500, 'internal_error', '模拟内部错误：devmock error 场景注入（数据库连接失败）')
}

/** mock resolver 的最小请求信息（兼容 msw resolver 入参） */
export interface MockRequestInfo {
  request: Request
  params: Record<string, string | readonly string[] | undefined>
}

type MockResolver = (info: MockRequestInfo) => Response | Promise<Response>

/** 包一层 error 场景：当前场景为 error 时任何端点直接回统一 500 错误体 */
function withScenario(resolver: MockResolver): MockResolver {
  return (info) => {
    if (getMockScenario() === 'error') {
      return internalError()
    }
    return resolver(info)
  }
}

/** 相对路径补任意 origin 通配：浏览器同源与 Node 测试端（无 location）都能匹配 */
function toPattern(path: string): string {
  return path.startsWith('/') ? `*${path}` : path
}

export function mockGet(path: string, resolver: MockResolver): HttpHandler {
  return http.get(toPattern(path), withScenario(resolver))
}

export function mockPost(path: string, resolver: MockResolver): HttpHandler {
  return http.post(toPattern(path), withScenario(resolver))
}

export function mockPut(path: string, resolver: MockResolver): HttpHandler {
  return http.put(toPattern(path), withScenario(resolver))
}

export function mockPatch(path: string, resolver: MockResolver): HttpHandler {
  return http.patch(toPattern(path), withScenario(resolver))
}

export function mockDelete(path: string, resolver: MockResolver): HttpHandler {
  return http.delete(toPattern(path), withScenario(resolver))
}

/**
 * 兜底 handler：未被任何真实 handler 命中的 /admin | /beacon 请求返回明确 JSON 404，
 * 避免 onUnhandledRequest:'bypass' 让请求落到 SPA 返回 index.html、前端 JSON.parse 崩
 * 「Unexpected token '<'」。必须置于全部真实 handler 之后（MSW 按顺序首个命中优先）。
 */
export const fallbackHandlers: HttpHandler[] = [
  http.all('*/admin/*', () => jsonError(404, 'not_implemented', '演示模式未实现该端点')),
  http.all('*/beacon/*', () => jsonError(404, 'not_implemented', '演示模式未实现该端点')),
]

/** 读取路径参数（msw params 值可能是数组，统一取首个） */
export function pathParam(info: MockRequestInfo, name: string): string {
  const value = info.params[name]
  if (typeof value === 'string') {
    return value
  }
  if (Array.isArray(value) && value.length > 0) {
    return String(value[0])
  }
  return ''
}

/** 读取 JSON 请求体（解析失败回空对象，由各 handler 做字段校验） */
export async function readBody<T>(request: Request): Promise<Partial<T>> {
  try {
    return (await request.json()) as Partial<T>
  } catch {
    return {}
  }
}

export interface PageOptions {
  /** 页码参数名，默认 page */
  pageParam?: string
  /** 页大小参数名，默认 pageSize（Legacy 端点用 size） */
  sizeParam?: string
  /** 默认页大小 */
  defaultSize?: number
}

/** 解析正整数 query 参数，非法或缺省回退默认值 */
export function queryInt(url: URL, name: string, fallback: number): number {
  const raw = url.searchParams.get(name)
  if (raw === null) {
    return fallback
  }
  const parsed = Number.parseInt(raw, 10)
  return Number.isNaN(parsed) || parsed <= 0 ? fallback : parsed
}

/** 读取字符串 query 参数（空串视为未传） */
export function queryStr(url: URL, name: string): string | null {
  const raw = url.searchParams.get(name)
  return raw === null || raw === '' ? null : raw
}

/** 在 handler 内真实生效的服务端分页 */
export function paginate<T>(all: readonly T[], url: URL, options?: PageOptions): Paged<T> {
  const page = queryInt(url, options?.pageParam ?? 'page', 1)
  const pageSize = queryInt(url, options?.sizeParam ?? 'pageSize', options?.defaultSize ?? 20)
  const start = (page - 1) * pageSize
  return { items: all.slice(start, start + pageSize), total: all.length }
}

/** 解析 RFC3339 时间参数为毫秒时间戳，非法返回 null */
export function queryTimeMs(url: URL, name: string): number | null {
  const raw = queryStr(url, name)
  if (raw === null) {
    return null
  }
  const ms = Date.parse(raw)
  return Number.isNaN(ms) ? null : ms
}
