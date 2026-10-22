// Agent 接入与配置页（/agent-settings，FR-279，系统大域）：控制面下发 agent 运行参数的唯一入口。
//
// 任务闭环：选作用域（namespace，可再收窄到某台 server）→ 看清每个键的生效值与来源 →
// 改要改的项 → 填原因一次性下发 → 看到「已下发，agent 将在约 30 秒内热生效」。
//
// 三条硬边界都做在界面上，不靠文档提醒：
// 1. 命令白名单是高危项（ADR-0011）：页顶常驻警告 + 行内高危药丸 + stringList 取值校验；
// 2. 本机权威键（接入目标 / 接入凭据 / 身份标识）一个都不进可编辑表，单独一区说明「只能本地配置」；
// 3. 高危键的下发可能被拦到审批（202 票据），页面必须区分「已下发」「已提交审批」「票据已终结」，
//    不能糊成一句成功——运维需要知道此刻到底生没生效。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { Button, TableSkeleton, Textarea } from '@beacon/ui'
import type { UpdateAgentSettingsBody } from '@beacon/contracts'

import {
  ApiClientError,
  fetchAgentSettings,
  readAgentSettingsApprovalTicket,
  updateAgentSettings,
} from '../api/agent-settings'
import { fetchNamespaces, fetchServers } from '../api/cluster'
import PageAsyncSection from '../features/shared/page-async-section'
import PageTitle from '../shell/page-title'
import HighRiskNotice from './agent-settings/high-risk-notice'
import LocalOnlyCard from './agent-settings/local-only-card'
import ScopeBar from './agent-settings/scope-bar'
import SettingsTable from './agent-settings/settings-table'
import {
  ISSUE_MESSAGE_KEY,
  agentSettingValueIssue,
  isForbiddenAgentSettingKey,
  isHighRiskAgentSetting,
  isRevocableAgentSettingSource,
  type CommandWhitelistIssue,
} from './agent-settings/keys'

// 服务器候选上限：只取一页并缓存，避免把上千子服塞进 DOM（与交付域 ServerPicker 同口径）
const SERVER_CANDIDATE_PAGE_SIZE = 200

/**
 * 票据状态归类。服务端 status 取值见 model.ApprovalStatus*：
 * pending / executing / rejected / withdrawn / expired / succeeded / failed。
 *
 * 必须读 status 而不是只看有没有 approvalRequestId：服务端命中幂等键时直接回既有申请、
 * **不判状态**，一张早已 rejected 的旧票据与刚创建的长得一模一样。只认 id 就会把「已终结」
 * 谎报成「已提交审批」，而那条变更其实再也提交不出去。
 */
type ApprovalOutcome =
  /** pending：还在等签发 */
  | 'pending'
  /** rejected / withdrawn / expired / failed：已终结，本次没有下发 */
  | 'terminal'
  /** succeeded：审批已执行完，库里已经变了 */
  | 'succeeded'
  /** 其余（含 executing 与未来新增值）：不猜，让运维去审批中心确认 */
  | 'unknown'

const TERMINAL_APPROVAL_STATUSES = ['rejected', 'withdrawn', 'expired', 'failed']

function approvalOutcome(status: string): ApprovalOutcome {
  const normalized = status.trim().toLowerCase()
  if (normalized === 'pending') {
    return 'pending'
  }
  if (TERMINAL_APPROVAL_STATUSES.includes(normalized)) {
    return 'terminal'
  }
  if (normalized === 'succeeded') {
    return 'succeeded'
  }
  return 'unknown'
}

// 下发结果的四种呈现：直执生效 / 待审批 / 票据已终结 / 票据已执行
type SaveResultView =
  | { kind: 'saved' }
  | { kind: 'approval'; requestId: string; status: string; outcome: ApprovalOutcome }

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function AgentSettingsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [namespaceId, setNamespaceId] = useState(0)
  const [serverId, setServerId] = useState('')
  // 行内草稿：key → 待下发值（不即时提交，由页脚统一下发）。
  // 值是稀疏的（只有编辑过的键才存在），故显式允许 undefined，避免误判「一定已设置」。
  // 草稿：undefined=未改；null=标记撤销本层覆盖；字符串=设为该值
  const [drafts, setDrafts] = useState<Record<string, string | null | undefined>>({})
  const [editingKey, setEditingKey] = useState<string | null>(null)
  const [reason, setReason] = useState('')
  const [formError, setFormError] = useState<string | null>(null)
  const [saveResult, setSaveResult] = useState<SaveResultView | null>(null)

  const namespaceQuery = useQuery({
    queryKey: ['agent-settings', 'namespaces'],
    queryFn: fetchNamespaces,
    staleTime: 60_000,
  })
  const namespaces = namespaceQuery.data?.items ?? []

  // 首次拿到候选即落在第一个 namespace：本页是「管理入口」，不选就没有内容可看。
  // 用户一旦自己改过选择（namespaceId > 0）就不再自动覆盖。
  const activeNamespaceId = namespaceId > 0 ? namespaceId : (namespaces[0]?.id ?? 0)

  const serverQuery = useQuery({
    queryKey: ['agent-settings', 'servers', activeNamespaceId],
    queryFn: () =>
      fetchServers({
        namespaceId: activeNamespaceId,
        kind: 'backend',
        pageSize: SERVER_CANDIDATE_PAGE_SIZE,
      }),
    enabled: activeNamespaceId > 0,
    staleTime: 60_000,
  })
  const servers = serverQuery.data?.items ?? []

  const valuesQuery = useQuery({
    queryKey: ['agent-settings', 'values', activeNamespaceId, serverId],
    queryFn: () =>
      fetchAgentSettings({
        namespaceId: activeNamespaceId,
        serverId: serverId === '' ? undefined : serverId,
      }),
    enabled: activeNamespaceId > 0,
  })
  const items = useMemo(() => valuesQuery.data?.items ?? [], [valuesQuery.data])

  // 写端点的二次确认串：只能原样回填 GET 给的 scopeLabel。
  // 绝不凭 namespaceId / serverId 自己拼——那样「以为在改这台服、实际改了整个环境」
  // 这道闸门就退化成前端自说自话，服务端也无从发现作用域搞错了。
  const scopeLabel = valuesQuery.data?.scopeLabel ?? ''

  // 禁止下发的键一律不进可编辑表：前端侧第二道闸（后端另有 400 拒发兜底）
  const editableItems = useMemo(() => items.filter((item) => !isForbiddenAgentSettingKey(item.key)), [items])
  // 高危以服务端 dangerous 字段为准，本地清单只在字段缺失时兜底（否则服务端新增危险键会漏报）
  const highRiskItems = useMemo(() => editableItems.filter(isHighRiskAgentSetting), [editableItems])

  // 只有「与当前生效值不同」的草稿才算改动：改回原值不该产生一条下发记录。
  //
  // 草稿有三种状态：
  //   - `undefined` → 没动过（不在 drafts 里）
  //   - `null`      → 标记为「撤销本层覆盖」，提交时传 null，服务端删掉这一层
  //   - 字符串      → 设为该值（空串在下面被拦下，它不等于撤销）
  const dirtyValues = useMemo(() => {
    const currentValueByKey = new Map(items.map((item) => [item.key, item.value]))
    const next: Record<string, string | null> = {}
    for (const [key, value] of Object.entries(drafts)) {
      if (value === undefined) {
        continue
      }
      if (value === null) {
        // 只有当前**确实有本层覆盖**时，撤销才是一个真实改动。
        // 本来就是「本地配置或代码默认」的键，撤销是无操作，不该产生一条空下发。
        const item = items.find((row) => row.key === key)
        if (item !== undefined && isRevocableAgentSettingSource(item.source)) {
          next[key] = null
        }
        continue
      }
      if (value === (currentValueByKey.get(key) ?? '')) {
        continue
      }
      next[key] = value
    }
    return next
  }, [drafts, items])
  const dirtyCount = Object.keys(dirtyValues).length

  // 取值问题就地拦下：白名单按 stringList 口径，其余键拦「清空」（清空不等于撤销，服务端 400）。
  // 撤销（null）无需校验取值——它不写任何值。
  const valueIssues = useMemo(() => {
    const issues: { key: string; issue: CommandWhitelistIssue }[] = []
    for (const [key, value] of Object.entries(dirtyValues)) {
      if (value === null) {
        continue
      }
      const item = items.find((row) => row.key === key)
      const issue = agentSettingValueIssue(item ?? { key }, value)
      if (issue !== null) {
        issues.push({ key, issue })
      }
    }
    return issues
  }, [dirtyValues, items])
  const firstIssue = valueIssues.at(0)

  const mutation = useMutation({
    mutationFn: (body: UpdateAgentSettingsBody) => updateAgentSettings(body),
    onSuccess: async (result) => {
      const ticket = readAgentSettingsApprovalTicket(result)
      setFormError(null)
      if (ticket === null) {
        // 200 直执：变更已落库，清空草稿与原因
        setDrafts({})
        setEditingKey(null)
        setReason('')
        setSaveResult({ kind: 'saved' })
      } else {
        const outcome = approvalOutcome(ticket.status)
        if (outcome === 'pending') {
          // 已受理、等签发：本次意图已经交出去了，清空草稿
          setDrafts({})
          setEditingKey(null)
          setReason('')
        }
        // 票据已终结 / 已执行 / 状态未知：库内没变，**保留草稿与原因**——
        // 运维要改的往往就是那句话（驳回理由常常指向它），重敲一遍值毫无意义
        setSaveResult({ kind: 'approval', requestId: ticket.approvalRequestId, status: ticket.status, outcome })
      }
      await queryClient.invalidateQueries({ queryKey: ['agent-settings', 'values'] })
    },
    onError: (error) => {
      // 失败时保留草稿与原因：重试不该让运维重新敲一遍
      setSaveResult(null)
      setFormError(t('system.agentSettings.save.failed', { message: messageOf(error) }))
    },
  })

  // 切作用域必须清空草稿：否则会把 A 服务器的改动带着下发到 B
  const resetTransientState = () => {
    setDrafts({})
    setEditingKey(null)
    setReason('')
    setFormError(null)
    setSaveResult(null)
  }

  const submit = () => {
    // 没有二次确认串就不发：空串过去只会换一个 400，且说不清是模板错了还是作用域错了
    if (scopeLabel === '') {
      setFormError(t('system.agentSettings.save.scopeUnconfirmed'))
      return
    }
    if (firstIssue !== undefined) {
      setFormError(t(ISSUE_MESSAGE_KEY[firstIssue.issue]))
      return
    }
    if (dirtyCount === 0) {
      setFormError(t('system.agentSettings.save.noChange'))
      return
    }
    if (reason.trim() === '') {
      setFormError(t('system.agentSettings.save.reasonRequired'))
      return
    }
    setFormError(null)
    setSaveResult(null)
    mutation.mutate({
      namespaceId: activeNamespaceId,
      serverId: serverId === '' ? undefined : serverId,
      confirmationScope: scopeLabel,
      values: dirtyValues,
      reason: reason.trim(),
    })
  }

  const submitDisabled =
    mutation.isPending || dirtyCount === 0 || firstIssue !== undefined || scopeLabel === ''

  return (
    <section className="grid gap-4">
      {/* 页面级 h1（sr-only）：本页无可见大标题，只补语义，不参与布局。
          说明文案放在作用域卡内（见 ScopeBar），避免可见标题与 h1 同文重复播报。 */}
      <PageTitle titleKey="nav.agentSettings" />

      <ScopeBar
        namespaces={namespaces}
        namespaceId={activeNamespaceId}
        onNamespaceChange={(next) => {
          setNamespaceId(next)
          setServerId('')
          resetTransientState()
        }}
        namespacePending={namespaceQuery.isLoading}
        namespaceError={namespaceQuery.isError ? messageOf(namespaceQuery.error) : null}
        onRetryNamespace={() => {
          void namespaceQuery.refetch()
        }}
        servers={servers}
        serverId={serverId}
        onServerChange={(next) => {
          setServerId(next)
          resetTransientState()
        }}
        serverPending={serverQuery.isLoading}
        updatedAt={valuesQuery.data?.updatedAt ?? ''}
      />

      {activeNamespaceId <= 0 ? (
        <p className="rounded-xl border border-border bg-card px-4 py-8 text-center text-sm text-ink-3">
          {namespaceQuery.isLoading
            ? t('system.agentSettings.scope.namespaceLoading')
            : t('system.agentSettings.scopeRequired')}
        </p>
      ) : (
        <>
          {/* 高危警告常驻：只要白名单可下发，风险就一直在，不能只在编辑时才提醒 */}
          {highRiskItems.length > 0 && <HighRiskNotice keys={highRiskItems.map((item) => item.key)} />}

          <div className="grid gap-0 rounded-xl border border-border bg-card shadow-card">
            <div className="border-b border-border px-4 py-3">
              <div className="flex flex-wrap items-baseline gap-2">
                <h2 className="text-[13px] font-semibold text-ink-1">{t('system.agentSettings.listTitle')}</h2>
                <span className="text-[11px] text-ink-4">
                  {t('system.agentSettings.listHint', { count: editableItems.length })}
                </span>
              </div>
            </div>

            <PageAsyncSection
              isLoading={valuesQuery.isLoading}
              isError={valuesQuery.isError}
              error={valuesQuery.error}
              loadingText={t('system.agentSettings.loadFail')}
              skeleton={<TableSkeleton columns={4} rows={6} />}
            >
              {editableItems.length === 0 ? (
                <p className="px-4 py-8 text-center text-sm text-ink-3">{t('system.agentSettings.empty')}</p>
              ) : (
                <SettingsTable
                  items={editableItems}
                  drafts={drafts}
                  editingKey={editingKey}
                  onEdit={(key) => {
                    const item = editableItems.find((row) => row.key === key)
                    setFormError(null)
                    setSaveResult(null)
                    setEditingKey(key)
                    // 编辑一个「已标记撤销」的行 = 放弃撤销、改成写入新值
                    setDrafts((prev) => {
                      const next = prev[key]
                      return { ...prev, [key]: typeof next === 'string' ? next : (item?.value ?? '') }
                    })
                  }}
                  onRevoke={(key) => {
                    // 标记撤销：提交时传 null，服务端删掉该层的覆盖行
                    setFormError(null)
                    setSaveResult(null)
                    setEditingKey((prev) => (prev === key ? null : prev))
                    setDrafts((prev) => ({ ...prev, [key]: null }))
                  }}
                  onUndoRevoke={(key) => {
                    // 取消撤销 = 丢弃该行草稿，恢复成未改动
                    setDrafts((prev) =>
                      Object.fromEntries(Object.entries(prev).filter(([draftKey]) => draftKey !== key)),
                    )
                  }}
                  onCancel={(key) => {
                    // 取消 = 丢弃该行草稿：用重建成对过滤实现，避免动态 delete
                    setDrafts((prev) =>
                      Object.fromEntries(Object.entries(prev).filter(([draftKey]) => draftKey !== key)),
                    )
                    setEditingKey((prev) => (prev === key ? null : prev))
                  }}
                  onDraftChange={(key, value) => {
                    setDrafts((prev) => ({ ...prev, [key]: value }))
                  }}
                />
              )}
            </PageAsyncSection>

            {/* 下发区：一次提交一次审批 / 审计，原因必填 */}
            <div className="grid gap-2 border-t border-border px-4 py-3">
              <span className="text-xs font-medium text-ink-2">{t('system.agentSettings.save.reason')}</span>
              <Textarea
                aria-label={t('system.agentSettings.save.reason')}
                placeholder={t('system.agentSettings.save.reasonPlaceholder')}
                value={reason}
                rows={2}
                onChange={(e) => {
                  setReason(e.target.value)
                }}
              />
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-xs text-ink-3">
                  {dirtyCount === 0
                    ? t('system.agentSettings.save.noChange')
                    : t('system.agentSettings.save.pendingCount', { count: dirtyCount })}
                </span>
                <Button
                  className="ml-auto"
                  size="sm"
                  disabled={submitDisabled}
                  onClick={submit}
                >
                  {mutation.isPending
                    ? t('system.agentSettings.save.submitting')
                    : t('system.agentSettings.save.submit', { count: dirtyCount })}
                </Button>
              </div>

              {/* 待下发却有作用域未确认：说明为什么按钮按不下去，而不是让它默默灰着 */}
              {scopeLabel === '' && dirtyCount > 0 && !valuesQuery.isLoading && (
                <p className="text-xs text-ink-3">{t('system.agentSettings.save.scopeUnconfirmed')}</p>
              )}

              {formError !== null && (
                <p role="alert" className="text-xs text-destructive">
                  {formError}
                </p>
              )}
              {saveResult?.kind === 'saved' && (
                <p
                  role="status"
                  className="rounded-lg border border-brand-100 bg-brand-50 px-3 py-2 text-sm text-ink-2"
                >
                  {t('system.agentSettings.save.saved')}
                </p>
              )}
              {saveResult?.kind === 'approval' && saveResult.outcome === 'pending' && (
                <div
                  role="status"
                  className="flex flex-wrap items-center gap-2 rounded-lg border border-brand-100 bg-brand-50 px-3 py-2 text-sm text-ink-2"
                >
                  <span>{t('system.agentSettings.save.submitted', { id: saveResult.requestId })}</span>
                  <Link
                    className="underline-offset-2 hover:underline"
                    to={`/approvals/${saveResult.requestId}`}
                  >
                    {t('system.agentSettings.save.viewApproval')}
                  </Link>
                </div>
              )}
              {saveResult?.kind === 'approval' &&
                (saveResult.outcome === 'terminal' ||
                  saveResult.outcome === 'succeeded' ||
                  saveResult.outcome === 'unknown') && (
                  <p
                    role="status"
                    className="rounded-lg border border-warn-bd bg-warn-bg px-3 py-2 text-sm text-ink-2"
                  >
                    {saveResult.outcome === 'terminal' &&
                      t('system.agentSettings.save.ticketTerminal', { status: saveResult.status })}
                    {saveResult.outcome === 'succeeded' && t('system.agentSettings.save.ticketSucceeded')}
                    {saveResult.outcome === 'unknown' &&
                      t('system.agentSettings.save.ticketUnknown', { status: saveResult.status })}
                  </p>
                )}
            </div>
          </div>
        </>
      )}

      <LocalOnlyCard />
    </section>
  )
}
