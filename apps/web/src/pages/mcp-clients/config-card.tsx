// MCP 入口配置只读卡：展示当前部署的启用状态、公网入口、信任边界与两个开关，
// 并提供部署文档入口。
//
// 这些配置全部是启动项（改后须重启控制面），管理台不提供写入——故本卡只有展示与文档跳转。
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ExternalLink, Info, Settings2 } from 'lucide-react'

import { AsyncSection, Badge, Skeleton } from '@beacon/ui'
import type { MCPConfigView } from '@beacon/contracts'

// 部署文档地址：后端不提供仓库内文档 URL，故指向仓库文档页；集中定义便于后续调整为内网镜像。
const DOCS_URL = 'https://github.com/wcpe/Beacon/blob/main/docs/OPERATIONS.md#9-mcp-反向代理验收'

interface ConfigCardProps {
  data: MCPConfigView | undefined
  isLoading: boolean
  isError: boolean
  error: unknown
}

export default function ConfigCard({ data, isLoading, isError, error }: ConfigCardProps) {
  const { t } = useTranslation()

  return (
    <section className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex flex-wrap items-center gap-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <Settings2 className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('system.mcpClients.config.title')}</h2>
        <a
          className="ml-auto inline-flex items-center gap-1 text-xs text-brand-600 hover:underline"
          href={DOCS_URL}
          rel="noreferrer"
          target="_blank"
        >
          {t('system.mcpClients.config.docsLink')}
          <ExternalLink className="size-3" />
        </a>
      </div>

      <AsyncSection
        isLoading={isLoading}
        isError={isError}
        error={error}
        skeleton={<Skeleton className="h-24 w-full" />}
      >
        {data && (data.enabled ? <EnabledRows data={data} /> : <DisabledHint />)}
      </AsyncSection>

      {/* 文档外链可能不可达（内网部署无外网），故同时给出可复制的文档路径；
          启动项提示常驻，避免误以为可在本页修改。 */}
      <p className="text-[11px] text-ink-4">{t('system.mcpClients.config.restartHint')}</p>
      <p className="text-[11px] text-ink-4">{t('system.mcpClients.config.docsPathHint')}</p>
    </section>
  )
}

// 已启用：逐项展示部署事实
function EnabledRows({ data }: { data: MCPConfigView }) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-2 text-sm sm:grid-cols-2">
      <Row label={t('system.mcpClients.config.status')}>
        <Badge variant="ok" className="gap-1.5">
          <span className="size-1.5 rounded-full bg-current" />
          {t('system.mcpClients.config.enabled')}
        </Badge>
      </Row>
      <Row label={t('system.mcpClients.config.publicBaseUrl')}>
        <span className="font-mono text-xs break-all text-ink-2">{data.publicBaseUrl || '—'}</span>
      </Row>
      <Row label={t('system.mcpClients.config.deployMode')}>
        <span className="text-sm text-ink-1">
          {data.directMode
            ? t('system.mcpClients.config.deployModeDirect')
            : t('system.mcpClients.config.deployModeProxy')}
        </span>
        {!data.directMode && (
          <span className="text-[11px] text-ink-4">
            {t('system.mcpClients.config.deployModeProxyHint', { count: data.trustedProxyCidrs.length })}
          </span>
        )}
      </Row>
      <Row label={t('system.mcpClients.config.allowedHosts')}>
        <span className="font-mono text-xs break-all text-ink-2">
          {data.allowedHosts.length === 0 ? '—' : data.allowedHosts.join(', ')}
        </span>
      </Row>
      <Row label={t('system.mcpClients.config.approvalDecide')}>
        <span className="text-sm text-ink-1">
          {data.allowApprovalDecide
            ? t('system.mcpClients.config.approvalDecideOn')
            : t('system.mcpClients.config.approvalDecideOff')}
        </span>
      </Row>
      <Row label={t('system.mcpClients.config.machineRegister')}>
        <span className="text-sm text-ink-1">
          {data.allowMachineRegister
            ? t('system.mcpClients.config.machineRegisterOn')
            : t('system.mcpClients.config.machineRegisterOff')}
        </span>
      </Row>
    </div>
  )
}

// 未启用：中性空态 + 配置指引（不当作错误）
function DisabledHint() {
  const { t } = useTranslation()
  return (
    <div className="flex items-start gap-2 rounded-lg border border-dashed border-border px-3 py-4 text-sm text-ink-3">
      <Info className="mt-0.5 size-4 shrink-0 text-ink-4" />
      <span>{t('system.mcpClients.config.disabledHint')}</span>
    </div>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-ink-4">{label}</span>
      <span className="flex flex-wrap items-center gap-2">{children}</span>
    </div>
  )
}
