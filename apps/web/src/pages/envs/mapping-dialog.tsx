// env→namespace 映射编辑弹窗（FR-178）：整体替换语义——勾选归入本 env 的 namespace。
// 已被其他 env 占用的 namespace 就地标注占用方；保存时若冲突，后端 409 的脱敏文案（含冲突方）经 errorText 内联展示。
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { EnvItem, NamespaceItem } from '@beacon/contracts'
import {
  Badge,
  Button,
  Checkbox,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@beacon/ui'

interface MappingDialogProps {
  open: boolean
  env: EnvItem | null
  namespaces: NamespaceItem[]
  allEnvs: EnvItem[]
  pending: boolean
  errorText: string | null
  onOpenChange: (open: boolean) => void
  onSubmit: (namespaceIds: number[]) => void
}

export default function MappingDialog({
  open,
  env,
  namespaces,
  allEnvs,
  pending,
  errorText,
  onOpenChange,
  onSubmit,
}: MappingDialogProps) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<Set<number>>(new Set())

  // 打开时用本 env 当前映射初始化勾选
  useEffect(() => {
    if (open && env) {
      setSelected(new Set(env.namespaces.map((ns) => ns.id)))
    }
  }, [open, env])

  // 各 namespace 的占用 env 名（非本 env）：用于就地标注「已属 env X」
  const ownerByNamespace = useMemo(() => {
    const map = new Map<number, string>()
    for (const other of allEnvs) {
      if (other.id === env?.id) {
        continue
      }
      for (const ns of other.namespaces) {
        map.set(ns.id, other.name)
      }
    }
    return map
  }, [allEnvs, env])

  const toggle = (id: number, checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (checked) {
        next.add(id)
      } else {
        next.delete(id)
      }
      return next
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('system.envs.mappingTitle')}</DialogTitle>
          <DialogDescription>{t('system.envs.mappingDesc')}</DialogDescription>
        </DialogHeader>
        <div className="grid max-h-72 gap-0.5 overflow-y-auto">
          {namespaces.length === 0 && <p className="text-sm text-ink-3">{t('system.envs.mappingEmpty')}</p>}
          {namespaces.map((ns) => {
            const owner = ownerByNamespace.get(ns.id)
            return (
              <label
                key={ns.id}
                className="flex cursor-pointer items-center gap-2.5 rounded-lg px-2 py-1.5 hover:bg-muted/50"
              >
                <Checkbox
                  checked={selected.has(ns.id)}
                  onCheckedChange={(v) => {
                    toggle(ns.id, v === true)
                  }}
                  aria-label={ns.name}
                />
                <span className="text-sm text-ink-1">{ns.name}</span>
                {owner !== undefined && (
                  <Badge variant="warn" className="ml-auto">
                    {t('system.envs.occupiedBy', { env: owner })}
                  </Badge>
                )}
              </label>
            )
          })}
        </div>
        {errorText && <p className="text-sm text-destructive">{errorText}</p>}
        <DialogFooter>
          <Button
            disabled={pending}
            onClick={() => {
              onSubmit([...selected])
            }}
          >
            {pending ? t('system.envs.mappingSaving') : t('system.envs.mappingSave')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
