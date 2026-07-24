// 通用响应壳：分页与统一错误体（docs/API.md 通用约定）。
// 真源：分页 page/pageSize（+keyword）响应 {items,total}；统一错误体 {code,message,traceId}。

/** 统一分页响应形状 {items,total} */
export interface Paged<T> {
  items: T[]
  total: number
}

/** 统一错误体（docs/API.md 通用约定） */
export interface MockErrorBody {
  code: string
  message: string
  traceId: string
}
