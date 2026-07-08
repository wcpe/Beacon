// 健康权重块：展示当前权重版本（因子权重 + 等级阈值）、编辑权重保存生成新 rev、rev 历史。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { History } from 'lucide-react'

import {
  AsyncSection,
  Button,
  Input,
  Label,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  TableSkeleton,
} from '@beacon/ui'
import type { HealthWeightsConfig } from '@beacon/devmock'

import { ApiClientError, fetchHealthWeights, putHealthWeights } from '../../api/system'
import { formatIso } from '../../features/system/format'

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function WeightsBlock() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<HealthWeightsConfig | null>(null)
  const [error, setError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['health-weights'],
    queryFn: fetchHealthWeights,
  })

  const mutation = useMutation({
    mutationFn: (config: HealthWeightsConfig) => putHealthWeights(config),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['health-weights'] })
      setEditing(false)
      setDraft(null)
    },
    onError: (err) => {
      setError(messageOf(err))
    },
  })

  const current = query.data?.current
  const weightKeys = useMemo(() => (current ? Object.keys(current.config.weights) : []), [current])

  const startEdit = () => {
    if (current) {
      setDraft(structuredClone(current.config))
      setError(null)
      setEditing(true)
    }
  }

  const setWeight = (factor: string, value: string) => {
    setDraft((prev) => {
      if (prev === null) {
        return prev
      }
      const num = Number.parseInt(value, 10)
      return { ...prev, weights: { ...prev.weights, [factor]: Number.isNaN(num) ? 0 : num } }
    })
  }

  const setLevel = (field: 'healthyMin' | 'degradedMin', value: string) => {
    setDraft((prev) => {
      if (prev === null) {
        return prev
      }
      const num = Number.parseInt(value, 10)
      return { ...prev, levels: { ...prev.levels, [field]: Number.isNaN(num) ? 0 : num } }
    })
  }

  return (
    <div className="grid gap-4 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex flex-row items-start justify-between gap-3">
        <div>
          <h3 className="text-[14px] font-semibold text-ink-1">{t('system.settings.weights.title')}</h3>
          <p className="mt-1 text-sm text-ink-3">{t('system.settings.weights.desc')}</p>
        </div>
        {!editing && current && (
          <Button size="sm" variant="outline" onClick={startEdit}>
            {t('system.settings.weights.edit')}
          </Button>
        )}
      </div>
      <div className="grid gap-4">
        <AsyncSection
          isLoading={query.isLoading}
          isError={query.isError}
          error={query.error}
          loadingText={t('system.settings.weights.loadFail')}
          skeleton={<TableSkeleton columns={2} rows={6} />}
        >
          {current && (
            <>
              <div className="flex flex-wrap items-center gap-2 rounded-lg bg-surface-2 px-3 py-2 text-sm">
                <span className="text-ink-3">{t('system.settings.weights.currentRev')}</span>
                <span className="font-semibold text-brand-600 tnum">#{current.rev}</span>
                <span className="text-ink-4">·</span>
                <span className="text-ink-2">{current.operator}</span>
                <span className="text-ink-4">·</span>
                <span className="text-ink-3 tnum">{formatIso(current.createdAt)}</span>
              </div>

              {/* 因子权重编辑 / 展示 */}
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
                {weightKeys.map((factor) => (
                  <div key={factor} className="grid gap-1 rounded-lg bg-surface-2 px-3 py-2">
                    <Label className="text-[11px] text-ink-4">{factor}</Label>
                    {editing && draft ? (
                      <Input
                        type="number"
                        aria-label={factor}
                        value={draft.weights[factor]}
                        onChange={(e) => {
                          setWeight(factor, e.target.value)
                        }}
                        className="h-8 w-20"
                      />
                    ) : (
                      <span className="text-[15px] font-semibold tabular-nums text-ink-1">
                        {current.config.weights[factor]}
                      </span>
                    )}
                  </div>
                ))}
              </div>

              {/* 等级阈值 */}
              <div className="grid grid-cols-2 gap-3">
                <div className="grid gap-1 rounded-lg bg-surface-2 px-3 py-2">
                  <Label className="text-[11px] text-ink-4">{t('system.settings.weights.healthyMin')}</Label>
                  {editing && draft ? (
                    <Input
                      type="number"
                      aria-label={t('system.settings.weights.healthyMin')}
                      value={draft.levels.healthyMin}
                      onChange={(e) => {
                        setLevel('healthyMin', e.target.value)
                      }}
                      className="h-8 w-24"
                    />
                  ) : (
                    <span className="text-[15px] font-semibold tabular-nums text-ink-1">
                      {current.config.levels.healthyMin}
                    </span>
                  )}
                </div>
                <div className="grid gap-1 rounded-lg bg-surface-2 px-3 py-2">
                  <Label className="text-[11px] text-ink-4">{t('system.settings.weights.degradedMin')}</Label>
                  {editing && draft ? (
                    <Input
                      type="number"
                      aria-label={t('system.settings.weights.degradedMin')}
                      value={draft.levels.degradedMin}
                      onChange={(e) => {
                        setLevel('degradedMin', e.target.value)
                      }}
                      className="h-8 w-24"
                    />
                  ) : (
                    <span className="text-[15px] font-semibold tabular-nums text-ink-1">
                      {current.config.levels.degradedMin}
                    </span>
                  )}
                </div>
              </div>

              {editing && (
                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    disabled={mutation.isPending}
                    onClick={() => {
                      if (draft) {
                        mutation.mutate(draft)
                      }
                    }}
                  >
                    {mutation.isPending
                      ? t('system.settings.weights.saving')
                      : t('system.settings.weights.save')}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setEditing(false)
                      setDraft(null)
                    }}
                  >
                    {t('system.settings.weights.cancel')}
                  </Button>
                  {error !== null && <span className="text-sm text-crit">{error}</span>}
                </div>
              )}

              {/* rev 历史 */}
              <div>
                <p className="mb-1.5 flex items-center gap-1.5 text-sm font-medium text-ink-1">
                  <History className="size-3.5 text-ink-4" />
                  {t('system.settings.weights.history')}
                </p>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>rev</TableHead>
                      <TableHead>{t('system.settings.weights.operator')}</TableHead>
                      <TableHead>{t('system.settings.weights.createdAt')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {[...(query.data?.history ?? [])].reverse().map((rev) => (
                      <TableRow key={rev.rev}>
                        <TableCell className="tabular-nums">#{rev.rev}</TableCell>
                        <TableCell>{rev.operator}</TableCell>
                        <TableCell>{formatIso(rev.createdAt)}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </>
          )}
        </AsyncSection>
      </div>
    </div>
  )
}
