// 交付大域数据获取共用底座（/assets /configs /changes /changes/history）：
// 复用集群域已导出的 ApiClientError（保证 instanceof 一致），请求封装走本域 request.ts。
// 各子域端点拆到 delivery-assets.ts / delivery-configs.ts / delivery-changes.ts。

import type { NamespaceListResponse } from '@beacon/devmock'

import { ApiClientError } from './cluster'
import { buildQuery, request } from './request'

export { ApiClientError, buildQuery, request }

/** 顶部作用域选择用的 namespace 列表（与集群域共用同一端点） */
export function fetchNamespaces(): Promise<NamespaceListResponse> {
  return request('GET', '/admin/v2/namespaces?pageSize=100')
}
