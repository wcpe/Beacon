// 测试共用的 msw Node 服务端：注册全量 handlers，并在用例间复位场景与数据仓。
import { setupServer } from 'msw/node'
import { allHandlers, resetMockData, setMockScenario } from '../index'

export const server = setupServer(...allHandlers)

/** 便捷请求封装：路径拼到测试域名上（handler 按路径匹配，与域名无关） */
export async function callJson(
  method: string,
  path: string,
  body?: unknown,
): Promise<{ status: number; json: unknown }> {
  const response = await fetch(`http://beacon.test${path}`, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await response.text()
  return { status: response.status, json: text === '' ? null : (JSON.parse(text) as unknown) }
}

/** 复位到常规态 + 重建数据仓（每个用例后调用） */
export function resetMockWorld(): void {
  setMockScenario('normal')
  resetMockData()
}
