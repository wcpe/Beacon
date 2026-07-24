// 运行参数块：Legacy 热改项按前缀分组，逐行读取 / 编辑 / 保存（保存成功热更生效）。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Gauge } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  TableSkeleton,
} from '@beacon/ui'
import type { SettingItem } from '@beacon/contracts'

import { ApiClientError, fetchSettings, updateSetting } from '../../api/system'

// 分组定义：前缀 → i18n 分组键
const GROUPS: { key: string; prefixes: string[] }[] = [
  { key: 'sampling', prefixes: ['metric.'] },
  { key: 'health', prefixes: ['health.'] },
  { key: 'longpoll', prefixes: ['longpoll.', 'alert.'] },
  { key: 'update', prefixes: ['update.', 'reverse-fetch.'] },
  { key: 'archive', prefixes: ['archive.'] },
  { key: 'other', prefixes: ['log.'] },
]

function groupOf(key: string): string {
  const hit = GROUPS.find((g) => g.prefixes.some((p) => key.startsWith(p)))
  return hit?.key ?? 'other'
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function SettingsBlock() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [editKey, setEditKey] = useState<string | null>(null)
  const [draft, setDraft] = useState('')
  const [rowError, setRowError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['settings', 'list'],
    queryFn: fetchSettings,
  })

  const mutation = useMutation({
    mutationFn: ({ key, value }: { key: string; value: string }) => updateSetting(key, value),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['settings'] })
      setEditKey(null)
    },
    onError: (error) => {
      setRowError(messageOf(error))
    },
  })

  // 按分组归拢
  const grouped = useMemo(() => {
    const map = new Map<string, SettingItem[]>()
    for (const g of GROUPS) {
      map.set(g.key, [])
    }
    for (const item of query.data?.items ?? []) {
      map.get(groupOf(item.key))?.push(item)
    }
    return map
  }, [query.data])

  const startEdit = (item: SettingItem) => {
    setRowError(null)
    setEditKey(item.key)
    setDraft(item.value)
  }

  const renderValueCell = (item: SettingItem) => {
    if (editKey !== item.key) {
      return item.value === '' ? <span className="text-muted-foreground">-</span> : <span>{item.value}</span>
    }
    if (item.valueType === 'bool') {
      return (
        <Select value={draft} onValueChange={setDraft}>
          <SelectTrigger className="w-28" aria-label={item.key}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="true">true</SelectItem>
            <SelectItem value="false">false</SelectItem>
          </SelectContent>
        </Select>
      )
    }
    return (
      <Input
        aria-label={item.key}
        value={draft}
        onChange={(e) => {
          setDraft(e.target.value)
        }}
        className="w-40"
      />
    )
  }

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      skeleton={<TableSkeleton columns={4} rows={6} />}
    >
      <div className="grid gap-4">
        {GROUPS.map((g) => {
          const items = grouped.get(g.key) ?? []
          if (items.length === 0) {
            return null
          }
          return (
            <div key={g.key} className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
              <div className="flex items-center gap-2">
                <span className="grid size-[26px] shrink-0 place-items-center rounded-lg bg-brand-50 text-brand" aria-hidden>
                  <Gauge className="size-[15px]" />
                </span>
                <h3 className="text-[13px] font-semibold text-ink-1">{t(`system.settings.groups.${g.key}`)}</h3>
              </div>
              <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t('system.settings.table.key')}</TableHead>
                      <TableHead>{t('system.settings.table.value')}</TableHead>
                      <TableHead>{t('system.settings.table.desc')}</TableHead>
                      <TableHead className="text-right">{t('system.settings.table.actions')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {items.map((item) => (
                      <TableRow key={item.key}>
                        <TableCell className="font-mono text-xs">{item.key}</TableCell>
                        <TableCell>
                          <div className="flex flex-col gap-1">
                            {renderValueCell(item)}
                            {editKey === item.key && rowError !== null && (
                              <span className="text-xs text-destructive">{rowError}</span>
                            )}
                          </div>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {t(`system.settings.descs.${item.key}`, { defaultValue: item.desc })}
                          {item.isStartup && (
                            <Badge variant="warn" className="ml-1">
                              {t('system.settings.table.startup')}
                            </Badge>
                          )}
                        </TableCell>
                        <TableCell className="text-right">
                          {editKey === item.key ? (
                            <div className="flex justify-end gap-1.5">
                              <Button
                                size="sm"
                                variant="ghost"
                                onClick={() => {
                                  setEditKey(null)
                                }}
                              >
                                {t('system.settings.table.cancel')}
                              </Button>
                              <Button
                                size="sm"
                                disabled={mutation.isPending}
                                onClick={() => {
                                  mutation.mutate({ key: item.key, value: draft })
                                }}
                              >
                                {mutation.isPending
                                  ? t('system.settings.table.saving')
                                  : t('system.settings.table.save')}
                              </Button>
                            </div>
                          ) : (
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() => {
                                startEdit(item)
                              }}
                            >
                              {t('system.settings.table.edit')}
                            </Button>
                          )}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
            </div>
          )
        })}
      </div>
    </AsyncSection>
  )
}
