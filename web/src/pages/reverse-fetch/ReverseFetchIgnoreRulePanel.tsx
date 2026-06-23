// 反向抓取持久忽略规则面板（FR-60）：列 / 建 / 删持久规则（ns/scope/group/target，
// ruleType exact[单文件] / prefix[目录前缀]，pattern）。命中规则的文件在审核台默认排除。

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { createIgnoreRule, deleteIgnoreRule, listIgnoreRules } from '../../api/client'
import type { IgnoreRuleType, ReverseFetchScope } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

export default function ReverseFetchIgnoreRulePanel({
  namespace,
  scope,
  group,
  target,
}: {
  // 规则作用域：与所选任务一致（建规则时带上，列表按此过滤）
  namespace: string
  scope: ReverseFetchScope
  group: string
  target?: string
}) {
  const { t } = useTranslation()
  const msg = useMessage()
  const qc = useQueryClient()
  // 新建表单：类型 + 匹配串 + 备注
  const [ruleType, setRuleType] = useState<IgnoreRuleType>('exact')
  const [pattern, setPattern] = useState('')
  const [comment, setComment] = useState('')

  const rulesQuery = useQuery({
    queryKey: ['reverse-fetch-ignore-rules', namespace, scope, group, target ?? ''],
    queryFn: () => listIgnoreRules({ namespace, scope, group, target }),
    enabled: !!namespace,
  })
  const rules = rulesQuery.data ?? []

  const createMut = useMutation({
    mutationFn: () =>
      createIgnoreRule({
        namespace,
        scope,
        group,
        target: scope === 'server' ? target : undefined,
        ruleType,
        pattern: pattern.trim(),
        comment: comment.trim() || undefined,
      }),
    onSuccess: () => {
      msg.showSuccess(t('reverseFetchTask.msgRuleCreated'))
      setPattern('')
      setComment('')
      qc.invalidateQueries({ queryKey: ['reverse-fetch-ignore-rules'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: (id: number) => deleteIgnoreRule(id),
    onSuccess: () => {
      msg.showSuccess(t('reverseFetchTask.msgRuleDeleted'))
      qc.invalidateQueries({ queryKey: ['reverse-fetch-ignore-rules'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!pattern.trim()) {
      msg.showError(t('reverseFetchTask.rulePatternRequired'))
      return
    }
    createMut.mutate()
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-card p-3">
      <div className="flex items-center gap-2">
        <div className="text-sm font-medium">{t('reverseFetchTask.rulesTitle')}</div>
      </div>
      <p className="text-xs text-muted-foreground">{t('reverseFetchTask.rulesHint')}</p>

      {/* 新建规则表单 */}
      <form onSubmit={onCreate} className="flex flex-wrap items-end gap-3">
        <div className="space-y-1.5">
          <Label htmlFor="rft-rule-type" className="text-xs">
            {t('reverseFetchTask.ruleTypeLabel')}
          </Label>
          <select
            id="rft-rule-type"
            className="h-8 w-36 rounded border border-input bg-background px-2 text-sm"
            value={ruleType}
            onChange={(e) => setRuleType(e.target.value as IgnoreRuleType)}
          >
            <option value="exact">{t('reverseFetchTask.ruleTypeExact')}</option>
            <option value="prefix">{t('reverseFetchTask.ruleTypePrefix')}</option>
          </select>
        </div>
        <div className="flex-1 min-w-[14rem] space-y-1.5">
          <Label htmlFor="rft-rule-pattern" className="text-xs">
            {t('reverseFetchTask.rulePatternLabel')}
          </Label>
          <Input
            id="rft-rule-pattern"
            className="h-8 font-mono"
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
            placeholder={t('reverseFetchTask.rulePatternPlaceholder')}
          />
        </div>
        <div className="min-w-[10rem] space-y-1.5">
          <Label htmlFor="rft-rule-comment" className="text-xs">
            {t('reverseFetchTask.ruleCommentLabel')}
          </Label>
          <Input
            id="rft-rule-comment"
            className="h-8"
            value={comment}
            onChange={(e) => setComment(e.target.value)}
          />
        </div>
        <Button type="submit" disabled={createMut.isPending}>
          {createMut.isPending
            ? t('reverseFetchTask.addingRule')
            : t('reverseFetchTask.addRuleBtn')}
        </Button>
      </form>

      {/* 规则列表 */}
      {rules.length === 0 ? (
        <div className="rounded border border-dashed border-border px-3 py-4 text-center text-xs text-muted-foreground">
          {t('reverseFetchTask.emptyRules')}
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-24">{t('reverseFetchTask.colRuleType')}</TableHead>
              <TableHead>{t('reverseFetchTask.colRulePattern')}</TableHead>
              <TableHead>{t('reverseFetchTask.colRuleScope')}</TableHead>
              <TableHead>{t('reverseFetchTask.colRuleComment')}</TableHead>
              <TableHead className="text-right">{t('reverseFetchTask.colActions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rules.map((r) => (
              <TableRow key={r.id}>
                <TableCell>
                  <Badge variant="outline" className="text-[0.65rem]">
                    {r.ruleType === 'prefix'
                      ? t('reverseFetchTask.ruleTypePrefix')
                      : t('reverseFetchTask.ruleTypeExact')}
                  </Badge>
                </TableCell>
                <TableCell className="font-mono text-xs break-all">{r.pattern}</TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {r.scope === 'server' ? `${r.group} · ${r.target || '-'}` : r.group}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">{r.comment || '-'}</TableCell>
                <TableCell className="text-right">
                  <Button
                    size="xs"
                    variant="destructive"
                    disabled={deleteMut.isPending}
                    onClick={() => deleteMut.mutate(r.id)}
                  >
                    {t('reverseFetchTask.deleteRuleBtn')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}
