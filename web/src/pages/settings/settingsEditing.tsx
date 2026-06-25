// 设置逐项编辑共享原语（FR-62/FR-77 抽取，供 OpsSettingsBlock 与 SystemConfigBlock 复用，FR-101）：
// 集中草稿 + dirty 计算 + 批量保存的 useSettingsDraft，与逐项编辑行 SettingRow / 控件 SettingControl /
// 改动摘要 ChangeSummary。把原 OpsSettingsBlock 内私有实现上提为单一真源，消除跨块复制（避免复制粘贴反模式）。

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { listSettings, updateSetting } from '../../api/client'
import type { SettingView } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

// log.level 的合法枚举（硬编码，后端白名单同此集合）。
const LOG_LEVELS = ['ERROR', 'WARN', 'INFO', 'DEBUG'] as const
// update.channel 的合法枚举（与后端 updateChannels 同口径，FR-101）。
const UPDATE_CHANNELS = ['stable', 'rc'] as const

// 取 key 的前缀段（第一个 `.` 前）——按域分桶用。
export function prefixOf(key: string): string {
  const i = key.indexOf('.')
  return i < 0 ? key : key.slice(0, i)
}

// useSettingsDraft 持有「全量设置 + 集中草稿 + dirty + 批量保存」——草稿统一以字符串持有（bool 用 'true'/'false'），
// 跨子 tab 统观全部脏项（草稿不下沉进单个子 tab）。供一个块内多子 tab 共享同一份草稿与批量保存。
export function useSettingsDraft() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['settings'],
    queryFn: listSettings,
  })

  // 草稿值集中持有于块顶层（key → 字符串草稿）。
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
        next[it.key] = cur !== undefined && cur !== it.value ? cur : it.value
      }
      return next
    })
  }, [data])

  const setDraft = (key: string, value: string) =>
    setDrafts((prev) => ({ ...prev, [key]: value }))

  const items = data ?? []
  const draftOf = (item: SettingView) => drafts[item.key] ?? item.value
  // 脏项 = 草稿 ≠ 当前生效值（跨全部子 tab）。
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

  return { items, isLoading, isError, error, draftOf, setDraft, dirtyItems, savingAll, saveAll }
}

// 块内 sticky 底栏：跨子 tab 统一改动摘要 + 批量保存（脏项汇总块内全部子 tab）。
export function SettingsSaveBar({
  dirtyItems,
  draftOf,
  savingAll,
  saveAll,
  summaryTestId,
}: {
  dirtyItems: SettingView[]
  draftOf: (item: SettingView) => string
  savingAll: boolean
  saveAll: () => void
  summaryTestId: string
}) {
  const { t } = useTranslation()
  return (
    <div className="sticky bottom-0 shrink-0 space-y-3 border-t bg-background pt-4">
      {dirtyItems.length > 0 && (
        <ChangeSummary items={dirtyItems} draftOf={draftOf} testId={summaryTestId} />
      )}
      <Button disabled={dirtyItems.length === 0 || savingAll} onClick={saveAll}>
        {savingAll ? t('settings.savingAll') : t('settings.saveAll', { count: dirtyItems.length })}
      </Button>
    </div>
  )
}

// 改动摘要：逐脏项列出「key：旧值 → 新值」。testId 区分实例。
export function ChangeSummary({
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
    <div data-testid={testId} className="rounded-md border bg-muted/40 p-3 text-xs">
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
// 受控：草稿由块顶层集中持有，便于批量保存 / 改动摘要统观全部脏项。
export function SettingRow({
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
        <Button size="sm" disabled={!dirty || busy} onClick={() => saveMut.mutate(draft)}>
          {saveMut.isPending ? t('settings.saving') : t('settings.saveBtn')}
        </Button>
      </div>
    </div>
  )
}

// 按 valueType 渲染编辑控件：log.level / update.channel 特例下拉；bool 开关（复选）；int 数字输入；string 文本输入。
export function SettingControl({
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
      <EnumSelect options={LOG_LEVELS} draft={draft} onChange={onChange} width="w-32" />
    )
  }

  // update.channel 特例：固定枚举下拉（stable/rc，FR-101）
  if (item.key === 'update.channel') {
    return (
      <EnumSelect options={UPDATE_CHANNELS} draft={draft} onChange={onChange} width="w-32" />
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

  // string（log.level / update.channel 以外）：普通文本输入
  return (
    <Input
      type="text"
      className="w-56"
      value={draft}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}

// 固定枚举下拉（log.level / update.channel 共用，消除两处下拉复制）。
function EnumSelect({
  options,
  draft,
  onChange,
  width,
}: {
  options: readonly string[]
  draft: string
  onChange: (value: string) => void
  width: string
}) {
  return (
    <Select value={draft} onValueChange={onChange}>
      <SelectTrigger className={width}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((opt) => (
          <SelectItem key={opt} value={opt}>
            {opt}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
