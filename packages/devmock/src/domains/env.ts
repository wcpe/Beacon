// env 展示维度 mock（/admin/v2/envs*）。
// 契约真源：docs/specs/v2-zone-authority.md §3.4 / §4.1 / §5。
// env 是纯展示 / 过滤维度：env→namespace 映射整体替换（PUT 幂等），一个 namespace 至多属一个 env（冲突 409 指明冲突方）。
// error 码与后端 apperr 对齐：INVALID_PARAM / ENV_CONFLICT / ENV_NOT_FOUND / ENV_NAMESPACE_CONFLICT / ENV_NAMESPACE_NOT_FOUND。

import { HttpResponse, type HttpHandler } from 'msw'
import type { EnvItem, EnvListResponse, EnvNamespaceRef } from '@beacon/contracts'
import {
  jsonError,
  mockDelete,
  mockGet,
  mockPatch,
  mockPost,
  mockPut,
  paginate,
  pathParam,
  queryStr,
  readBody,
} from '../http'
import { getClusterState, type ClusterState, type EnvRow } from '../data/cluster'
import { isoOffset } from '../support'

/** 沿 namespace id 解析展示名（名取 namespace.name/code，与 /namespaces 列表口径一致） */
function namespaceName(state: ClusterState, id: number): string {
  return state.namespaces.find((ns) => ns.id === id)?.name ?? `ns-${String(id)}`
}

/** env 行 → 契约视图（补映射 namespace 摘要 + 计数） */
function toEnvItem(state: ClusterState, env: EnvRow): EnvItem {
  const refs: EnvNamespaceRef[] = env.namespaceIds.map((id) => ({ id, name: namespaceName(state, id) }))
  return {
    id: env.id,
    name: env.name,
    description: env.description,
    namespaces: refs,
    namespaceCount: refs.length,
    createdAt: env.createdAt,
    updatedAt: env.updatedAt,
  }
}

interface CreateEnvBody {
  name?: string
  description?: string
}

interface UpdateEnvBody {
  name?: string
  description?: string
}

interface SetNamespacesBody {
  namespaceIds?: number[]
}

/** 去重并保序（容忍前端重复传入的 namespace id） */
function dedupe(ids: number[]): number[] {
  const seen = new Set<number>()
  const out: number[] = []
  for (const id of ids) {
    if (id > 0 && !seen.has(id)) {
      seen.add(id)
      out.push(id)
    }
  }
  return out
}

export const envHandlers: HttpHandler[] = [
  // env 列表（keyword 搜索 + 分页 + 映射 namespace 摘要）
  mockGet('/admin/v2/envs', ({ request }) => {
    const url = new URL(request.url)
    const keyword = queryStr(url, 'keyword')?.toLowerCase() ?? null
    const state = getClusterState()
    const rows: EnvItem[] = state.envs
      .filter((env) => keyword === null || env.name.toLowerCase().includes(keyword))
      .map((env) => toEnvItem(state, env))
    const { items, total } = paginate(rows, url)
    return HttpResponse.json({ items, total } satisfies EnvListResponse)
  }),

  // 创建 env（name 全局唯一）
  mockPost('/admin/v2/envs', async ({ request }) => {
    const body = await readBody<CreateEnvBody>(request)
    const name = body.name?.trim()
    if (!name) {
      return jsonError(400, 'INVALID_PARAM', 'name 必填')
    }
    const state = getClusterState()
    if (state.envs.some((e) => e.name === name)) {
      return jsonError(409, 'ENV_CONFLICT', `同名 env「${name}」已存在`)
    }
    const id = state.envs.reduce((max, e) => Math.max(max, e.id), 0) + 1
    const row: EnvRow = {
      id,
      name,
      description: body.description ?? '',
      namespaceIds: [],
      createdAt: isoOffset(0),
      updatedAt: isoOffset(0),
    }
    state.envs.push(row)
    return HttpResponse.json(toEnvItem(state, row), { status: 201 })
  }),

  // 改 env 名 / 描述（PATCH 局部更新；name 未传不改、描述未传不改）
  mockPatch('/admin/v2/envs/:id', async (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const state = getClusterState()
    const env = state.envs.find((e) => e.id === id)
    if (!env) {
      return jsonError(404, 'ENV_NOT_FOUND', 'env 不存在')
    }
    const body = await readBody<UpdateEnvBody>(info.request)
    if (body.name !== undefined) {
      const name = body.name.trim()
      if (!name) {
        return jsonError(400, 'INVALID_PARAM', 'name 不能为空')
      }
      if (state.envs.some((e) => e.id !== id && e.name === name)) {
        return jsonError(409, 'ENV_CONFLICT', `同名 env「${name}」已存在`)
      }
      env.name = name
    }
    if (body.description !== undefined) {
      env.description = body.description
    }
    env.updatedAt = isoOffset(0)
    return HttpResponse.json(toEnvItem(state, env))
  }),

  // 删 env（映射级联删除——env 行连同其 namespaceIds 一并移除，不影响任何权威数据）
  mockDelete('/admin/v2/envs/:id', (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const state = getClusterState()
    const idx = state.envs.findIndex((e) => e.id === id)
    if (idx < 0) {
      return jsonError(404, 'ENV_NOT_FOUND', 'env 不存在')
    }
    state.envs.splice(idx, 1)
    return new HttpResponse(null, { status: 204 })
  }),

  // 整体替换 env→namespace 映射（PUT 幂等）：不存在 namespace 400；被其他 env 占用 409 指明冲突方
  mockPut('/admin/v2/envs/:id/namespaces', async (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const state = getClusterState()
    const env = state.envs.find((e) => e.id === id)
    if (!env) {
      return jsonError(404, 'ENV_NOT_FOUND', 'env 不存在')
    }
    const body = await readBody<SetNamespacesBody>(info.request)
    const wanted = dedupe(body.namespaceIds ?? [])
    for (const nsId of wanted) {
      if (!state.namespaces.some((ns) => ns.id === nsId)) {
        return jsonError(400, 'ENV_NAMESPACE_NOT_FOUND', `待映射 namespace（id=${String(nsId)}）不存在`)
      }
    }
    const conflicts: string[] = []
    for (const other of state.envs) {
      if (other.id === id) {
        continue
      }
      for (const nsId of wanted) {
        if (other.namespaceIds.includes(nsId)) {
          conflicts.push(`${namespaceName(state, nsId)}（env「${other.name}」）`)
        }
      }
    }
    if (conflicts.length > 0) {
      conflicts.sort()
      return jsonError(
        409,
        'ENV_NAMESPACE_CONFLICT',
        `以下 namespace 已归属其他 env，请先从对方移除：${conflicts.join('、')}`,
      )
    }
    env.namespaceIds = wanted
    env.updatedAt = isoOffset(0)
    return HttpResponse.json(toEnvItem(state, env))
  }),
]
