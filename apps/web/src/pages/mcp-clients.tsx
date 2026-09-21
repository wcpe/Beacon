// MCP 客户端页（/mcp-clients，系统大域）：外部 Agent 接入凭据的日常运维。
//
// 布局沿用已拍板的主从范式（UX.md §5）：吸顶工具条 + 列表自区滚 + 右侧非模态详情面板。
// 四个生命周期动作按语义分档：
//   - 申请创建 / 轮换 / 启用 = 提审（202 + 审批票据），需填写原因与幂等键；
//   - 立即吊销 = 止损直执，二次确认后立即生效。
// MCP 入口配置是启动项，仅只读展示 + 文档跳转。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { Plug } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  DestructiveConfirmDialog,
  PageHeader,
  SummaryStrip,
  TableSkeleton,
  type DataTableColumn,
  type SummaryItem,
} from '@beacon/ui'
import type { CreateMCPClientBody, MCPClientItem } from '@beacon/contracts'

import {
  ApiClientError,
  createMcpClient,
  enableMcpClient,
  fetchMcpClients,
  fetchMcpConfig,
  revokeMcpClient,
  rotateMcpClient,
} from '../api/mcp'
import { formatIso } from '../features/system/format'
import ListCard from '../features/shared/list-card'
import MasterDetail from '../features/shared/master-detail'
import PlaintextDialog from '../features/shared/plaintext-dialog'
import ConfigCard from './mcp-clients/config-card'
import CreateDialog from './mcp-clients/create-dialog'
import DetailPanel from './mcp-clients/detail-panel'
import ReasonDialog, { type ReasonAction } from './mcp-clients/reason-dialog'

// 一次性明文弹窗内容
interface PlaintextView {
  title: string
  plaintext: string
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

// 状态 → 语义药丸：生效绿 / 已吊销红。
function statusTone(status: MCPClientItem['status']): 'ok' | 'crit' {
  return status === 'active' ? 'ok' : 'crit'
}

export default function McpClientsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [createOpen, setCreateOpen] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [reasonAction, setReasonAction] = useState<ReasonAction | null>(null)
  const [reasonTarget, setReasonTarget] = useState<MCPClientItem | null>(null)
  const [reasonError, setReasonError] = useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<MCPClientItem | null>(null)
  const [revokeError, setRevokeError] = useState<string | null>(null)
  const [plaintext, setPlaintext] = useState<PlaintextView | null>(null)
  // 提审成功但未拿到明文（幂等重放）时的提示
  const [approvalNotice, setApprovalNotice] = useState<{ requestId: string; missingSecret: boolean } | null>(null)
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const query = useQuery({ queryKey: ['mcp-clients'], queryFn: fetchMcpClients })
  const configQuery = useQuery({ queryKey: ['mcp-config'], queryFn: fetchMcpConfig })

  const items = useMemo(() => query.data?.items ?? [], [query.data])
  const selected = useMemo(
    () => items.find((item) => item.clientId === selectedId) ?? null,
    [items, selectedId],
  )

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['mcp-clients'] })

  // 申请创建：202 票据；clientSecret 只在首次响应出现
  const createMutation = useMutation({
    mutationFn: (body: CreateMCPClientBody) => createMcpClient(body, crypto.randomUUID()),
    onSuccess: async (ticket) => {
      await invalidate()
      setCreateOpen(false)
      if (ticket.clientSecret !== undefined && ticket.clientSecret !== '') {
        setPlaintext({ title: t('system.mcpClients.plaintextCreatedTitle'), plaintext: ticket.clientSecret })
      } else {
        // 幂等重放：服务端未生成新明文，必须明示而非静默
        setApprovalNotice({ requestId: ticket.approvalRequestId, missingSecret: true })
      }
    },
    onError: (error) => {
      setCreateError(messageOf(error))
    },
  })

  // 申请轮换：批准后产出新明文
  const rotateMutation = useMutation({
    mutationFn: (vars: { row: MCPClientItem; reason: string }) =>
      rotateMcpClient(vars.row.clientId, vars.reason, crypto.randomUUID()),
    onSuccess: async (ticket) => {
      await invalidate()
      setReasonAction(null)
      setReasonTarget(null)
      if (ticket.clientSecret !== undefined && ticket.clientSecret !== '') {
        setPlaintext({ title: t('system.mcpClients.plaintextRotatedTitle'), plaintext: ticket.clientSecret })
      } else {
        setApprovalNotice({ requestId: ticket.approvalRequestId, missingSecret: true })
      }
    },
    onError: (error) => {
      setReasonError(messageOf(error))
    },
  })

  const enableMutation = useMutation({
    mutationFn: (vars: { row: MCPClientItem; reason: string }) =>
      enableMcpClient(vars.row.clientId, vars.reason, crypto.randomUUID()),
    onSuccess: async (ticket) => {
      await invalidate()
      setReasonAction(null)
      setReasonTarget(null)
      setApprovalNotice({ requestId: ticket.approvalRequestId, missingSecret: false })
    },
    onError: (error) => {
      setReasonError(messageOf(error))
    },
  })

  // 吊销为直执（无审批票据）
  const revokeMutation = useMutation({
    mutationFn: (row: MCPClientItem) => revokeMcpClient(row.clientId),
    onSuccess: async () => {
      await invalidate()
      setRevokeTarget(null)
      setSelectedId(null)
    },
    onError: (error) => {
      setRevokeError(messageOf(error))
    },
  })

  const columns = useMemo<DataTableColumn<MCPClientItem>[]>(
    () => [
      {
        header: t('system.mcpClients.columns.displayName'),
        cell: (row) => <span className="font-medium">{row.displayName}</span>,
      },
      {
        header: t('system.mcpClients.columns.profile'),
        cell: (row) => t(`system.mcpClients.profile.${row.profile}`),
      },
      {
        header: t('system.mcpClients.columns.secretPrefix'),
        cell: (row) => <span className="font-mono text-xs">{row.secretPrefix}…</span>,
      },
      {
        header: t('system.mcpClients.columns.secretVersion'),
        cell: (row) => <span className="tnum text-xs">v{row.secretVersion}</span>,
      },
      {
        header: t('system.mcpClients.columns.status'),
        cell: (row) => (
          <Badge variant={statusTone(row.status)} className="gap-1.5">
            <span className="size-1.5 rounded-full bg-current" />
            {t(`system.mcpClients.status.${row.status}`)}
          </Badge>
        ),
      },
      {
        header: t('system.mcpClients.columns.createdAt'),
        cell: (row) => <span className="text-xs text-ink-3">{formatIso(row.createdAt)}</span>,
      },
    ],
    [t],
  )

  const summary = useMemo<SummaryItem[]>(() => {
    const active = items.filter((item) => item.status === 'active').length
    return [
      { label: t('system.mcpClients.summary.total'), value: items.length },
      { label: t('system.mcpClients.summary.active'), value: active },
      { label: t('system.mcpClients.summary.revoked'), value: items.length - active },
    ]
  }, [items, t])

  return (
    <section className="grid gap-4">
      <PageHeader
        icon={<Plug className="size-4" />}
        title={t('nav.mcpClients')}
      />

      <ConfigCard
        data={configQuery.data}
        error={configQuery.error}
        isError={configQuery.isError}
        isLoading={configQuery.isLoading}
      />

      {/* 提审回执：提醒申请需要人工批准才会生效 */}
      {approvalNotice && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-brand-100 bg-brand-50 px-3 py-2 text-sm text-brand">
          <span>
            {approvalNotice.missingSecret
              ? t('system.mcpClients.plaintextMissing')
              : t('system.mcpClients.approvalSubmitted')}
          </span>
          <Link
            className="inline-flex items-center gap-0.5 underline-offset-2 hover:underline"
            to={`/approvals/${approvalNotice.requestId}`}
          >
            {t('system.mcpClients.viewApproval')}
          </Link>
          <Button
            className="ml-auto"
            size="sm"
            variant="ghost"
            onClick={() => {
              setApprovalNotice(null)
            }}
          >
            {t('system.mcpClients.plaintextClose')}
          </Button>
        </div>
      )}

      <SummaryStrip items={summary} />

      <MasterDetail
        closeLabel={t('system.mcpClients.plaintextClose')}
        detail={
          selected && (
            <DetailPanel
              item={selected}
              onEnable={(row) => {
                setReasonError(null)
                setReasonTarget(row)
                setReasonAction('enable')
              }}
              onRevoke={(row) => {
                setRevokeError(null)
                setRevokeTarget(row)
              }}
              onRotate={(row) => {
                setReasonError(null)
                setReasonTarget(row)
                setReasonAction('rotate')
              }}
            />
          )
        }
        detailTitle={t('system.mcpClients.detailTitle')}
        master={
          <ListCard
            toolbar={
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-[13px] font-semibold text-ink-1">
                  {t('system.mcpClients.listTitle')}
                </span>
                <span className="text-[11px] text-ink-4">
                  {t('system.mcpClients.summary.total')} {items.length}
                </span>
                <Button
                  className="ml-auto"
                  size="sm"
                  onClick={() => {
                    setCreateError(null)
                    setCreateOpen(true)
                  }}
                >
                  {t('system.mcpClients.create')}
                </Button>
              </div>
            }
          >
            <AsyncSection
              error={query.error}
              isError={query.isError}
              isLoading={query.isLoading}
              loadingText={t('system.mcpClients.loadFail')}
              skeleton={<TableSkeleton columns={6} rows={5} />}
            >
              {items.length === 0 ? (
                <p className="px-1 py-8 text-center text-sm text-ink-3">{t('system.mcpClients.empty')}</p>
              ) : (
                <DataTable
                  columns={columns}
                  onRowClick={(row) => {
                    setSelectedId(row.clientId)
                  }}
                  rows={items}
                  rowKey={(row) => row.clientId}
                />
              )}
            </AsyncSection>
          </ListCard>
        }
        onClose={() => {
          setSelectedId(null)
        }}
      />

      <CreateDialog
        errorText={createError}
        onOpenChange={(open) => {
          setCreateOpen(open)
          if (!open) {
            setCreateError(null)
          }
        }}
        onSubmit={(body) => {
          // 提交前清空上次错误：否则重试期间旧错误仍显示，会误导用户以为新提交也失败了。
          setCreateError(null)
          createMutation.mutate(body)
        }}
        open={createOpen}
        pending={createMutation.isPending}
      />

      <ReasonDialog
        action={reasonAction}
        errorText={reasonError}
        onOpenChange={(open) => {
          if (!open) {
            setReasonAction(null)
            setReasonTarget(null)
            setReasonError(null)
          }
        }}
        onSubmit={(reasonInput) => {
          if (reasonTarget === null) {
            return
          }
          setReasonError(null)
          if (reasonAction === 'enable') {
            enableMutation.mutate({ row: reasonTarget, reason: reasonInput })
          } else {
            rotateMutation.mutate({ row: reasonTarget, reason: reasonInput })
          }
        }}
        pending={rotateMutation.isPending || enableMutation.isPending}
        targetName={reasonTarget?.displayName ?? ''}
      />

      <DestructiveConfirmDialog
        confirmLabel={t('system.mcpClients.confirmRevoke')}
        description={t('system.mcpClients.confirmRevokeDesc')}
        errorText={revokeError}
        onConfirm={() => {
          if (revokeTarget !== null) {
            revokeMutation.mutate(revokeTarget)
          }
        }}
        onOpenChange={(open) => {
          if (!open) {
            setRevokeTarget(null)
            setRevokeError(null)
          }
        }}
        open={revokeTarget !== null}
        pending={revokeMutation.isPending}
        title={t('system.mcpClients.confirmRevokeTitle', { name: revokeTarget?.displayName ?? '' })}
      />

      <PlaintextDialog
        keysPrefix="system.mcpClients"
        onOpenChange={(open) => {
          if (!open) {
            setPlaintext(null)
          }
        }}
        open={plaintext !== null}
        plaintext={plaintext?.plaintext ?? ''}
        title={plaintext?.title ?? ''}
      />
    </section>
  )
}
