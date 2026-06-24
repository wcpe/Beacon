// 运维设置页（FR-62，消费 FR-61 设置端点）：
// 分组展示热改项 + 逐项编辑 + 逐项保存 + 热生效回显；启动 / 安全项在 config.yml（此处不可见不可改）。
// 数据真源在后端白名单（GET /admin/v1/settings）；本页只按 valueType 选编辑控件、保存后刷新该列表。

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { listSettings, updateSetting } from '../api/client'
import type { SettingView } from '../api/types'
import { useMessage } from '../components/useMessage'
import AsyncSection from '@/components/AsyncSection'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

// log.level 的合法枚举（硬编码，后端白名单同此集合）。
const LOG_LEVELS = ['ERROR', 'WARN', 'INFO', 'DEBUG'] as const

// key 前缀 → i18n 组标题键的映射（前缀取第一个 `.` 前的段）。
const GROUP_TITLE_KEYS: Record<string, string> = {
  health: 'settings.groupHealth',
  metric: 'settings.groupMetric',
  longpoll: 'settings.groupLongpoll',
  alert: 'settings.groupAlert',
  log: 'settings.groupLog',
  'reverse-fetch': 'settings.groupReverseFetch',
}

// 取 key 的前缀段（第一个 `.` 前）。
function prefixOf(key: string): string {
  const i = key.indexOf('.')
  return i < 0 ? key : key.slice(0, i)
}

// 按前缀把设置项分组，保持后端返回的相对顺序（组顺序按首次出现）。
function groupByPrefix(items: SettingView[]): Array<{ prefix: string; items: SettingView[] }> {
  const order: string[] = []
  const buckets = new Map<string, SettingView[]>()
  for (const it of items) {
    const p = prefixOf(it.key)
    if (!buckets.has(p)) {
      buckets.set(p, [])
      order.push(p)
    }
    buckets.get(p)!.push(it)
  }
  return order.map((p) => ({ prefix: p, items: buckets.get(p)! }))
}

export default function SettingsPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['settings'],
    queryFn: listSettings,
  })

  // 草稿值集中持有于顶层（key → 字符串草稿），让页脚批量保存 / 改动摘要能看见全部草稿。
  // 草稿统一以字符串持有（bool 用 'true' / 'false'），与提交 / 比对口径一致。
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  // 批量保存进行态（禁用全部交互入口）。
  const [savingAll, setSavingAll] = useState(false)

  // 列表加载 / 刷新后，把「未被本地改动」的项草稿同步到最新当前值；保留仍在编辑的脏项草稿不被覆盖。
  useEffect(() => {
    if (!data) return
    setDrafts((prev) => {
      const next: Record<string, string> = {}
      for (const it of data) {
        const cur = prev[it.key]
        // 仍是脏草稿（与上一次当前值不同）则保留；否则跟随最新当前值。
        next[it.key] = cur !== undefined && cur !== it.value ? cur : it.value
      }
      return next
    })
  }, [data])

  const setDraft = (key: string, value: string) =>
    setDrafts((prev) => ({ ...prev, [key]: value }))

  const items = data ?? []
  const draftOf = (item: SettingView) => drafts[item.key] ?? item.value
  // 脏项 = 草稿 ≠ 当前生效值。
  const dirtyItems = items.filter((it) => draftOf(it) !== it.value)

  // 批量保存：逐个 PUT 复用 updateSetting，单项失败计数不抛、不影响其余项；完成后刷新列表并出汇总提示。
  const saveAll = async () => {
    if (dirtyItems.length === 0 || savingAll) return
    setSavingAll(true)
    let ok = 0
    let fail = 0
    for (const it of dirtyItems) {
      try {
        await updateSetting(it.key, draftOf(it))
        ok++
      } catch {
        fail++
      }
    }
    setSavingAll(false)
    qc.invalidateQueries({ queryKey: ['settings'] })
    if (fail === 0) {
      msg.showSuccess(t('settings.msgBatchSaved', { ok }))
    } else {
      msg.showError(t('settings.msgBatchPartial', { ok, fail }))
    }
  }

  const groups = data ? groupByPrefix(data) : []

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold">{t('settings.title')}</h1>
        {/* 启动 / 安全项在 config.yml 的说明：登录后不可见、此处不可改 */}
        <p className="mt-1 text-sm text-muted-foreground">{t('settings.configYmlNotice')}</p>
      </div>

      <AsyncSection isLoading={isLoading} isError={isError} error={error}>
        {groups.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('settings.empty')}</p>
        ) : (
          <div className="space-y-6">
            {/* 顶部改动摘要（仅有脏项时显示） */}
            {dirtyItems.length > 0 && (
              <ChangeSummary items={dirtyItems} draftOf={draftOf} testId="change-summary-top" />
            )}

            {groups.map((g) => (
              <Card key={g.prefix}>
                <CardHeader>
                  <CardTitle>
                    <h2>
                      {GROUP_TITLE_KEYS[g.prefix]
                        ? t(GROUP_TITLE_KEYS[g.prefix])
                        : t('settings.groupOther', { prefix: g.prefix })}
                    </h2>
                  </CardTitle>
                </CardHeader>
                <CardContent className="divide-y">
                  {g.items.map((item) => (
                    <SettingRow
                      key={item.key}
                      item={item}
                      draft={draftOf(item)}
                      onChange={(v) => setDraft(item.key, v)}
                      batchSaving={savingAll}
                    />
                  ))}
                </CardContent>
              </Card>
            ))}

            {/* 页脚：改动摘要 + 批量保存 */}
            <div className="space-y-3 border-t pt-4">
              {dirtyItems.length > 0 && (
                <ChangeSummary items={dirtyItems} draftOf={draftOf} testId="change-summary" />
              )}
              <Button
                disabled={dirtyItems.length === 0 || savingAll}
                onClick={saveAll}
              >
                {savingAll
                  ? t('settings.savingAll')
                  : t('settings.saveAll', { count: dirtyItems.length })}
              </Button>
            </div>
          </div>
        )}
      </AsyncSection>
    </div>
  )
}

// 改动摘要：逐脏项列出「key：旧值 → 新值」。testId 区分顶部 / 页脚两处实例。
function ChangeSummary({
  items,
  draftOf,
  testId,
}: {
  items: SettingView[]
  draftOf: (item: SettingView) => string
  testId: string
}) {
  const { t } = useTranslation()
  return (
    <div
      data-testid={testId}
      className="rounded-md border bg-muted/40 p-3 text-xs"
    >
      <div className="mb-1 font-medium">{t('settings.changeSummaryTitle')}</div>
      <ul className="space-y-0.5 font-mono text-muted-foreground">
        {items.map((it) => (
          <li key={it.key}>
            {t('settings.changeSummaryLine', { key: it.key, from: it.value, to: draftOf(it) })}
          </li>
        ))}
      </ul>
    </div>
  )
}

// 单条设置项一行：desc 标签 + key + 默认值提示 + 按 valueType 的编辑控件 + 恢复默认 + 保存按钮（值未变禁用）。
// 受控：草稿由顶层 SettingsPage 集中持有，便于页脚批量保存 / 改动摘要统观全部脏项。
function SettingRow({
  item,
  draft,
  onChange,
  batchSaving,
}: {
  item: SettingView
  draft: string
  onChange: (value: string) => void
  batchSaving: boolean
}) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()

  const saveMut = useMutation({
    mutationFn: (value: string) => updateSetting(item.key, value),
    onSuccess: () => {
      msg.showSuccess(t('settings.msgSaved'))
      // 刷新列表回显热生效后的值
      qc.invalidateQueries({ queryKey: ['settings'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 值未变（或保存中 / 批量保存中）禁用保存
  const dirty = draft !== item.value
  // 草稿已等于默认值时无可恢复改动，禁用「恢复默认」
  const atDefault = draft === item.default
  const busy = saveMut.isPending || batchSaving

  return (
    <div data-setting-row className="flex flex-wrap items-center gap-x-4 gap-y-2 py-3">
      <div className="min-w-0 flex-1">
        <div className="text-sm">{item.desc}</div>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
          <code className="font-mono">{item.key}</code>
          <span>{t('settings.defaultHint', { value: item.default })}</span>
        </div>
      </div>
      <div className="flex items-center gap-3">
        <SettingControl item={item} draft={draft} onChange={onChange} />
        <Button
          variant="outline"
          size="sm"
          disabled={atDefault || busy}
          onClick={() => onChange(item.default)}
        >
          {t('settings.resetDefault')}
        </Button>
        <Button
          size="sm"
          disabled={!dirty || busy}
          onClick={() => saveMut.mutate(draft)}
        >
          {saveMut.isPending ? t('settings.saving') : t('settings.saveBtn')}
        </Button>
      </div>
    </div>
  )
}

// 按 valueType 渲染编辑控件：log.level 特例下拉；bool 开关（复选）；int 数字输入；string 文本输入。
function SettingControl({
  item,
  draft,
  onChange,
}: {
  item: SettingView
  draft: string
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()

  // log.level 特例：固定枚举下拉
  if (item.key === 'log.level') {
    return (
      <Select value={draft} onValueChange={onChange}>
        <SelectTrigger className="w-32">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {LOG_LEVELS.map((lvl) => (
            <SelectItem key={lvl} value={lvl}>
              {lvl}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    )
  }

  if (item.valueType === 'bool') {
    const checked = draft === 'true'
    return (
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={checked}
          onCheckedChange={(v) => onChange(v === true ? 'true' : 'false')}
        />
        <span className="text-muted-foreground">
          {checked ? t('settings.boolOn') : t('settings.boolOff')}
        </span>
      </label>
    )
  }

  if (item.valueType === 'int') {
    return (
      <Input
        type="number"
        className="w-40"
        value={draft}
        onChange={(e) => onChange(e.target.value)}
      />
    )
  }

  // string（log.level 以外）：普通文本输入
  return (
    <Input
      type="text"
      className="w-56"
      value={draft}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}
