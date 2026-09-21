// server 标签编辑弹窗（FR-227）：列出既有标签（可删），并可新增 key=value；key 唯一、重复覆盖。
// 增改走 PUT /tags（按 key upsert）、删除走 DELETE /tags/{key}，均低风险直执 + 强审计。
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2 } from 'lucide-react'

import { Badge, Button, Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, Input, Label } from '@beacon/ui'
import type { ServerItem } from '@beacon/contracts'

import { ApiClientError, deleteServerTag, setServerTags } from '../../api/cluster'

const TAG_KEY_PATTERN = /^[A-Za-z0-9_.-]+$/
const KEY_MAX = 32
const VALUE_MAX = 128

export default function TagDialog({
  server,
  onOpenChange,
  onSaved,
}: {
  server: ServerItem | null
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const [key, setKey] = useState('')
  const [value, setValue] = useState('')
  const [errorText, setErrorText] = useState<string | null>(null)
  const tags = server?.tags ?? []
  const serverId = server?.serverId ?? ''

  const onError = (error: unknown) => {
    setErrorText(error instanceof ApiClientError ? error.message : String(error))
  }
  const saveMutation = useMutation({
    mutationFn: (payload: Record<string, string>) => setServerTags(serverId, payload),
    onSuccess: () => {
      onSaved()
      setKey('')
      setValue('')
      setErrorText(null)
    },
    onError,
  })
  const deleteMutation = useMutation({
    mutationFn: (tagKey: string) => deleteServerTag(serverId, tagKey),
    onSuccess: () => {
      onSaved()
      setErrorText(null)
    },
    onError,
  })

  const add = () => {
    const k = key.trim()
    if (k === '' || !TAG_KEY_PATTERN.test(k) || k.length > KEY_MAX || value.length > VALUE_MAX) {
      setErrorText(t('cluster.servers.tags.invalid'))
      return
    }
    saveMutation.mutate({ [k]: value })
  }

  return (
    <Dialog
      open={server !== null}
      onOpenChange={(open) => {
        if (!open) {
          setKey('')
          setValue('')
          setErrorText(null)
        }
        onOpenChange(open)
      }}
    >
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('cluster.servers.tags.title')}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="flex flex-wrap gap-1.5">
            {tags.length === 0 ? (
              <p className="text-[12.5px] text-ink-4">{t('cluster.servers.tags.empty')}</p>
            ) : (
              tags.map((tag) => (
                <Badge key={tag.key} variant="secondary" className="gap-1.5">
                  <span className="font-mono">
                    {tag.key}={tag.value}
                  </span>
                  <button
                    type="button"
                    aria-label={t('cluster.servers.tags.remove', { key: tag.key })}
                    className="text-ink-4 transition-colors hover:text-crit disabled:opacity-50"
                    disabled={deleteMutation.isPending}
                    onClick={() => {
                      deleteMutation.mutate(tag.key)
                    }}
                  >
                    <Trash2 className="size-3" />
                  </button>
                </Badge>
              ))
            )}
          </div>
          <div className="grid grid-cols-[1fr_1fr_auto] items-end gap-2">
            <div className="grid gap-1">
              <Label className="text-xs text-ink-3" htmlFor="tag-key">
                {t('cluster.servers.tags.key')}
              </Label>
              <Input
                id="tag-key"
                value={key}
                onChange={(e) => {
                  setKey(e.target.value)
                }}
                placeholder="env"
                className="h-8"
              />
            </div>
            <div className="grid gap-1">
              <Label className="text-xs text-ink-3" htmlFor="tag-value">
                {t('cluster.servers.tags.value')}
              </Label>
              <Input
                id="tag-value"
                value={value}
                onChange={(e) => {
                  setValue(e.target.value)
                }}
                placeholder="beta"
                className="h-8"
              />
            </div>
            <Button size="sm" className="gap-1" disabled={saveMutation.isPending} onClick={add}>
              <Plus className="size-3.5" />
              {t('cluster.servers.tags.add')}
            </Button>
          </div>
          {errorText !== null && <p className="text-[12px] text-crit">{errorText}</p>}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('cluster.servers.confirm.cancel')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
