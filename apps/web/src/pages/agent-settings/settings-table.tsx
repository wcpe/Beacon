// 可下发配置表（/agent-settings）：逐行「键 / 生效值 / 来源 / 操作」，行内编辑、批量下发。
//
// 编辑是「暂存」而非即时写：改动先落进页面的草稿（drafts），由页脚统一填原因提交一次，
// 与 PUT 的批量语义（values: Record<string,string>）对齐——一次下发就是一次审批 / 审计事件，
// 不应该变成每敲一个字符一条记录。
import { useTranslation } from 'react-i18next'

import {
  Badge,
  Button,
  Input,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  cn,
} from '@beacon/ui'
import type { AgentSettingItem, AgentSettingSource } from '@beacon/contracts'

import { ISSUE_MESSAGE_KEY, agentSettingValueIssue, isHighRiskAgentSetting, isRevocableAgentSettingSource } from './keys'

// 药丸色调的种类
type SourceVariant = 'brand' | 'outline' | 'off'

// 来源 → 药丸色调：下发行最抢眼（要去 Web 改），不可区分的本地档最弱（要去机器上改）。
//
// 键类型刻意用 Record<AgentSettingSource, ...>（而不是 Record<string, ...>）：
// 契约每加一档来源，这个对象就编译不过，逼着改的人当场决定新档「该怎么显示」，
// 而不是让它悄悄落到兜底色里、变成一个没人认领的灰药丸。
//
// 运行期仍可能出现本版本不认识的取值（控制面比前端新），但那不是靠放宽本对象的键类型
// 来兜的——本仓没开 noUncheckedIndexedAccess，两种写法取值都不会是 undefined；真正的
// 兜底在 sourceVariant 里用 KNOWN_SOURCES 收口，未识别取值明确显示「来源未知」。
const SOURCE_VARIANT: Record<AgentSettingSource, SourceVariant> = {
  server: 'brand',
  namespace: 'outline',
  'local-or-default': 'off',
  // 旧服务端 / 旧契约取值，语义与 local-or-default 完全相同（见契约 AgentSettingSource）
  unset: 'off',
  default: 'off',
}

// 本版本认识的来源取值（含旧取值）。不在其中的一律按「未知」呈现。
const KNOWN_SOURCES = new Set<string>(['server', 'namespace', 'local-or-default', 'unset', 'default'])

function isKnownSource(source: string): source is AgentSettingSource {
  return KNOWN_SOURCES.has(source)
}

// 取药丸色调：未识别来源兜底到 off 档。
//
// 为什么要这个函数而不是直接 `SOURCE_VARIANT[item.source]`：未识别的来源会让药丸样式塌成
// 无样式的一团字。配置来源是运维用来决定「去哪改」的依据，这一列不能因为版本错配就失形。
function sourceVariant(source: string): SourceVariant {
  return isKnownSource(source) ? SOURCE_VARIANT[source] : 'off'
}

// 来源文案的 i18n 键：旧取值折到 local-or-default；未识别取值明说「未知」，
// 不硬套成某个已知档——来源判断错一个档，运维就会去错的地方改配置。
function sourceLabelKey(source: string): string {
  if (!isKnownSource(source)) {
    return 'system.agentSettings.source.unknown'
  }
  const normalized = source === 'unset' || source === 'default' ? 'local-or-default' : source
  return `system.agentSettings.source.${normalized}`
}

export interface SettingsTableProps {
  items: AgentSettingItem[]
  /**
   * 已暂存的草稿：key → 待下发值（稀疏，未编辑的键不存在）。
   *
   * - `undefined` = 未改动
   * - `null` = 标记「撤销本层覆盖」，提交时传 null，服务端删掉这一层
   * - `string` = 设为该值
   */
  drafts: Record<string, string | null | undefined>
  /** 当前处于编辑态的行；null 表示没有行在编辑 */
  editingKey: string | null
  onEdit: (key: string) => void
  onCancel: (key: string) => void
  onDraftChange: (key: string, value: string) => void
  /** 标记撤销本层覆盖（提交时传 null） */
  onRevoke: (key: string) => void
  /** 取消撤销标记，恢复成未改动 */
  onUndoRevoke: (key: string) => void
}

export default function SettingsTable({
  items,
  drafts,
  editingKey,
  onEdit,
  onCancel,
  onDraftChange,
  onRevoke,
  onUndoRevoke,
}: SettingsTableProps) {
  const { t } = useTranslation()

  return (
    <>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('system.agentSettings.table.key')}</TableHead>
            <TableHead>{t('system.agentSettings.table.value')}</TableHead>
            <TableHead>{t('system.agentSettings.table.source')}</TableHead>
            <TableHead className="text-right">{t('system.agentSettings.table.actions')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((item) => {
            // 高危以服务端字段为准（本地清单只兜底字段缺失），见 keys.isHighRiskAgentSetting
            const highRisk = isHighRiskAgentSetting(item)
            const editing = editingKey === item.key
            const draft = drafts[item.key]
            // 撤销标记与「改了值」是两种不同的暂存形态，展示与校验都要分开处理
            const revoking = draft === null
            const staged = !revoking && draft !== undefined && draft !== item.value
            // 行内取值校验：撤销不写值，无需校验；其余按 stringList / 非空口径
            const issue = draft === undefined || revoking ? null : agentSettingValueIssue(item, draft)
            // 只有当前层真有覆盖时才提供撤销：否则是无操作，不应产生一条空下发
            const revocable = isRevocableAgentSettingSource(item.source) && !revoking

            return (
              <TableRow key={item.key}>
                <TableCell>
                  <div className="grid gap-0.5">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="font-mono text-xs text-ink-1">{item.key}</span>
                      {highRisk && <Badge variant="crit">{t('system.agentSettings.highRisk.badge')}</Badge>}
                    </div>
                    <span className="text-xs text-ink-4">
                      {t(`system.agentSettings.descs.${item.key}`, { defaultValue: item.desc })}
                    </span>
                  </div>
                </TableCell>

                <TableCell>
                  <div className="grid gap-1">
                    {editing ? (
                      <Input
                        aria-label={item.key}
                        aria-invalid={issue !== null}
                        value={draft ?? item.value}
                        onChange={(e) => {
                          onDraftChange(item.key, e.target.value)
                        }}
                        className="w-full max-w-md font-mono text-xs"
                      />
                    ) : (
                      <span
                        className={cn(
                          'font-mono text-xs break-all',
                          item.value === '' ? 'text-ink-4' : 'text-ink-1',
                        )}
                      >
                        {item.value === '' ? t('system.agentSettings.table.unset') : item.value}
                      </span>
                    )}
                    {issue !== null && (
                      <span className="text-xs text-destructive">{t(ISSUE_MESSAGE_KEY[issue])}</span>
                    )}
                    {revoking && (
                      <span className="text-xs text-ink-4">
                        {t('system.agentSettings.table.revokePending')}
                      </span>
                    )}
                    {staged && (
                      <span className="flex flex-wrap items-center gap-1.5 text-xs">
                        <Badge variant="brand">{t('system.agentSettings.table.pending')}</Badge>
                        <span
                          className={cn(
                            'font-mono break-all',
                            issue === null ? 'text-brand-600' : 'text-ink-4',
                          )}
                        >
                          {draft === '' ? t('system.agentSettings.table.blank') : draft}
                        </span>
                      </span>
                    )}
                  </div>
                </TableCell>

                <TableCell>
                  <Badge variant={sourceVariant(item.source)}>{t(sourceLabelKey(item.source))}</Badge>
                </TableCell>

                <TableCell className="text-right">
                  <div className="flex flex-wrap items-center justify-end gap-1">
                    {revoking ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => {
                          onUndoRevoke(item.key)
                        }}
                      >
                        {t('system.agentSettings.table.undoRevoke')}
                      </Button>
                    ) : (
                      <>
                        {editing ? (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => {
                              onCancel(item.key)
                            }}
                          >
                            {t('system.agentSettings.table.cancel')}
                          </Button>
                        ) : (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => {
                              onEdit(item.key)
                            }}
                          >
                            {t('system.agentSettings.table.edit')}
                          </Button>
                        )}
                        {revocable && (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => {
                              onRevoke(item.key)
                            }}
                          >
                            {t('system.agentSettings.table.revoke')}
                          </Button>
                        )}
                      </>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
      {/* 来源说明：这一列存在的意义是让运维判断「去哪改」，故把导向写死在表下 */}
      <p className="border-t border-border px-4 py-2 text-[11px] text-ink-4">
        {t('system.agentSettings.source.legend')}
      </p>
    </>
  )
}
