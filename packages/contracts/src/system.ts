// 系统域响应契约：控制面健康、版本与更新、密钥、运维设置。
// 沿用 docs/API.md Legacy /admin/v1 契约形状。

/** GET /admin/v1/system/status 响应（Legacy 形状） */
export interface SystemStatus {
  version: string
  startedAt: string
  uptimeSeconds: number
  db: { connected: boolean; error?: string }
  onlineInstances: number
  samplerEnabled: boolean
  runtime: { goroutines: number; heapAlloc: number; heapSys: number }
  cpuAvailable: boolean
  cpuPercent: number
}

/** GET /admin/v1/system/observability 响应（Legacy 形状） */
export interface SystemObservability {
  dbPool: {
    maxOpenConnections: number
    openConnections: number
    inUse: number
    idle: number
    waitCount: number
    waitDurationMs: number
  }
  longpoll: { config: number; file: number; topology: number; command: number; total: number }
  registryByStatus: Record<string, number>
  registryTotal: number
  commandByStatus: Record<string, number>
}

/** GET /admin/v1/system/update-check 响应（Legacy 形状） */
export interface UpdateCheck {
  status: 'ok' | 'check-failed'
  failureReason?: string
  currentVersion: string
  channel: 'stable'
  hasUpdate: boolean
  isDevBuild: boolean
  latestVersion: string
  releaseNotes: string
  releaseUrl: string
  publishedAt: string
  checkedAt: string
  cacheExpiresAt: string
}

export type UpdatePhase = 'idle' | 'checking' | 'downloading' | 'verifying' | 'staging' | 'ready-restart' | 'failed'

/** GET /admin/v1/system/update 响应（更新进度内存态） */
export interface UpdateProgress {
  phase: UpdatePhase
  percent: number
  targetVersion: string
  error: string
  rollbackAvailable: boolean
}

/** API 密钥列表项（无明文 / 哈希） */
export interface ApiKeyItem {
  id: number
  name: string
  role: 'full' | 'readonly'
  keyPrefix: string
  status: 'active' | 'expired' | 'revoked'
  createdAt: string
  expiresAt: string | null
  lastUsedAt: string | null
}

/** 运维设置项 */
export interface SettingItem {
  key: string
  value: string
  valueType: 'int' | 'bool' | 'string'
  default: string
  desc: string
  isStartup: boolean
}
