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

import { isHighRiskAgentSettingKey, validateCommandWhitelist } from './keys'

// 来源 → 药丸色调：server 覆盖最抢眼，未设置最弱
const SOURCE_VARIANT: Record<AgentSettingSource, 'brand' | 'outline' | 'off'> = {
  server: 'brand',
  namespace: 'outline',
  default: 'off',
}

export interface SettingsTableProps {
  items: AgentSettingItem[]
  /** 已暂存的草稿：key → 待下发值（稀疏，未编辑的键不存在） */
  drafts: Record<string, string | undefined>
  /** 当前处于编辑态的行；null 表示没有行在编辑 */
  editingKey: string | null
  onEdit: (key: string) => void
  onCancel: (key: string) => void
  onDraftChange: (key: string, value: string) => void
}

export default function SettingsTable({
  items,
  drafts,
  editingKey,
  onEdit,
  onCancel,
  onDraftChange,
}: SettingsTableProps) {
  const { t } = useTranslation()

  return (
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
          const highRisk = isHighRiskAgentSettingKey(item.key)
          const editing = editingKey === item.key
          const draft = drafts[item.key]
          const staged = draft !== undefined && draft !== item.value
          // 白名单只在这一行做取值校验：它是唯一可执行命令的开关，发错值不该静默失败
          const whitelistInvalid = highRisk && draft !== undefined && !validateCommandWhitelist(draft)

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
                      aria-invalid={whitelistInvalid}
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
                  {whitelistInvalid && (
                    <span className="text-xs text-destructive">
                      {t('system.agentSettings.highRisk.invalidJson')}
                    </span>
                  )}
                  {staged && (
                    <span className="flex flex-wrap items-center gap-1.5 text-xs">
                      <Badge variant="brand">{t('system.agentSettings.table.pending')}</Badge>
                      <span className="font-mono break-all text-brand-600">
                        {draft === '' ? t('system.agentSettings.table.unset') : draft}
                      </span>
                    </span>
                  )}
                </div>
              </TableCell>

              <TableCell>
                <Badge variant={SOURCE_VARIANT[item.source]}>
                  {t(`system.agentSettings.source.${item.source}`)}
                </Badge>
              </TableCell>

              <TableCell className="text-right">
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
              </TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </Table>
  )
}
