// 新服接入引导向导（FR-85）：服务器页「添加服务器」入口。
// 两步——① 填身份（环境 / serverId / 角色 / 大区 / 地址），含 serverId 环境内查重拦截；
// ② 生成可复制的 agent config.yml identity 段 + run 脚本 env 段，并可选预建 zone 指派（仅 bukkit）。
// 纯前端、复用既有端点（查重 listInstances、指派 assignZone）；不新增后端。

import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { assignZone, listInstances } from '../../api/client'
import type { ComboboxOption } from '@/components/ui/combobox'
import { buildOnboardingSnippets } from '@/lib/agentOnboarding'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Combobox } from '@/components/ui/combobox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

// bukkit 角色编码（与后端 role 约定一致）；bungee 代理不进 zone 指派（FR-8/FR-35/FR-71）。
const ROLE_BUKKIT = 'bukkit'
const ROLE_BUNGEE = 'bungee'

interface AddServerWizardProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // 服务器页当前筛选的环境（作为初值；为空则向导内须先选环境）
  namespace: string
  // 环境候选（「编码 · 名称」，复用页面 nsOptions）
  nsOptions: ComboboxOption[]
  // 大区候选（复用页面 groupOptions）
  groupOptions: string[]
}

export default function AddServerWizard({
  open,
  onOpenChange,
  namespace,
  nsOptions,
  groupOptions,
}: AddServerWizardProps) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()

  // 步骤：1=填身份，2=生成片段 / 预建指派
  const [step, setStep] = useState(1)
  // 身份表单草稿
  const [ns, setNs] = useState(namespace)
  const [serverId, setServerId] = useState('')
  const [role, setRole] = useState(ROLE_BUKKIT)
  const [group, setGroup] = useState('')
  const [address, setAddress] = useState('')
  // serverId 重复提示（进入下一步前校验置位）
  const [dupError, setDupError] = useState(false)
  // 预建指派的小区草稿（仅步骤二·bukkit）
  const [zone, setZone] = useState('')

  // 每次打开时重置草稿（环境取页面筛选初值）
  useEffect(() => {
    if (open) {
      setStep(1)
      setNs(namespace)
      setServerId('')
      setRole(ROLE_BUKKIT)
      setGroup('')
      setAddress('')
      setDupError(false)
      setZone('')
    }
  }, [open, namespace])

  // 查重数据源：目标环境内已注册实例（按 namespace 过滤，避免跨环境误判）。
  const { data: instances } = useQuery({
    queryKey: ['instances', 'onboarding', ns],
    queryFn: () => listInstances({ namespace: ns || undefined }),
    enabled: open,
  })

  // serverId 是否与目标环境内已在册实例撞名（早期提示，非权威闸——真源唯一性由控制面注册时把关）。
  const isDuplicate = useMemo(() => {
    const id = serverId.trim()
    if (!id) return false
    return (instances ?? []).some((i) => i.serverId === id)
  }, [instances, serverId])

  // 步骤一必填齐全（地址、serverId、大区、环境）
  const step1Valid =
    ns.trim() !== '' && serverId.trim() !== '' && group.trim() !== '' && address.trim() !== ''

  // 生成接入片段（步骤二展示；按当前草稿派生）
  const snippets = useMemo(
    () =>
      buildOnboardingSnippets({
        namespace: ns.trim(),
        serverId: serverId.trim(),
        group: group.trim(),
        address: address.trim(),
      }),
    [ns, serverId, group, address],
  )

  // 预建 zone 指派（FR-71 既有端点）：仅 bukkit、填了 zone 才可点。
  const assignMut = useMutation({
    mutationFn: () =>
      assignZone({
        namespace: ns.trim(),
        serverId: serverId.trim(),
        group: group.trim(),
        zone: zone.trim(),
        note: t('servers.wizardAssignNote'),
      }),
    onSuccess: (a) => {
      msg.showSuccess(t('servers.wizardAssignDone', { serverId: a.serverId, zone: a.zone }))
      qc.invalidateQueries({ queryKey: ['assignments'] })
      qc.invalidateQueries({ queryKey: ['zone-summary'] })
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onNext() {
    if (!step1Valid) {
      msg.showError(t('servers.wizardRequiredFields'))
      return
    }
    if (isDuplicate) {
      setDupError(true)
      return
    }
    setDupError(false)
    setStep(2)
  }

  // 复制文本到剪贴板（复用 ApiKeysPage 的 navigator.clipboard 范式）。
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      msg.showSuccess(t('common.copiedToClipboard'))
    } catch {
      msg.showError(t('common.copyFailed'))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('servers.wizardTitle')}</DialogTitle>
          <DialogDescription>{t('servers.wizardDesc')}</DialogDescription>
        </DialogHeader>

        {step === 1 ? (
          <div className="grid gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="w-namespace">{t('common.namespace')}</Label>
              {/* 环境严格选自已存在环境（候选「编码 · 名称」） */}
              <Combobox
                id="w-namespace"
                aria-label={t('common.namespace')}
                value={ns}
                onChange={setNs}
                options={nsOptions}
                allowCustom={false}
                placeholder={t('common.pleaseSelect')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="w-serverId">{t('common.serverId')}</Label>
              <Input
                id="w-serverId"
                aria-label={t('common.serverId')}
                value={serverId}
                onChange={(e) => {
                  setServerId(e.target.value)
                  setDupError(false)
                }}
                placeholder={t('servers.wizardServerIdPlaceholder')}
                autoComplete="off"
                className="font-mono"
              />
              {/* serverId 环境内查重提示（实时 + 点下一步兜底） */}
              {(dupError || isDuplicate) && serverId.trim() !== '' && (
                <p className="text-xs text-destructive">
                  {t('servers.wizardServerIdDuplicate', { serverId: serverId.trim(), namespace: ns })}
                </p>
              )}
            </div>
            <div className="space-y-1.5">
              <Label>{t('common.role')}</Label>
              <Select value={role} onValueChange={setRole}>
                <SelectTrigger className="w-40" aria-label={t('common.role')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ROLE_BUKKIT}>bukkit</SelectItem>
                  <SelectItem value={ROLE_BUNGEE}>bungee</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="w-group">{t('common.group')}</Label>
              {/* 大区可编辑（允许新建大区提示值） */}
              <Combobox
                id="w-group"
                aria-label={t('common.group')}
                value={group}
                onChange={setGroup}
                options={groupOptions}
                allowCustom
                placeholder={t('servers.wizardGroupPlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="w-address">{t('common.address')}</Label>
              <Input
                id="w-address"
                aria-label={t('common.address')}
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                placeholder={t('servers.wizardAddressPlaceholder')}
                autoComplete="off"
                className="font-mono"
              />
            </div>
          </div>
        ) : (
          <div className="grid gap-4">
            <p className="text-sm text-muted-foreground">{t('servers.wizardSnippetHint')}</p>
            {/* config.yml identity 段 */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between">
                <Label>{t('servers.wizardConfigLabel')}</Label>
                <Button variant="outline" size="sm" onClick={() => copy(snippets.configYaml)}>
                  {t('servers.wizardCopyBtn')}
                </Button>
              </div>
              <pre className="max-h-48 overflow-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre">
                {snippets.configYaml}
              </pre>
            </div>
            {/* run 脚本 env 段 */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between">
                <Label>{t('servers.wizardEnvLabel')}</Label>
                <Button variant="outline" size="sm" onClick={() => copy(snippets.envScript)}>
                  {t('servers.wizardCopyBtn')}
                </Button>
              </div>
              <pre className="max-h-32 overflow-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre">
                {snippets.envScript}
              </pre>
            </div>
            {/* 可选预建 zone 指派：仅 bukkit（BC 代理不进 zone 指派，与后端校验一致） */}
            {role === ROLE_BUKKIT && (
              <div className="space-y-1.5 rounded-md border p-3">
                <Label htmlFor="w-zone">{t('servers.wizardZoneLabel')}</Label>
                <p className="text-xs text-muted-foreground">{t('servers.wizardZoneHint')}</p>
                <div className="flex items-end gap-2">
                  <Input
                    id="w-zone"
                    aria-label={t('common.zone')}
                    value={zone}
                    onChange={(e) => setZone(e.target.value)}
                    placeholder={t('servers.wizardZonePlaceholder')}
                    autoComplete="off"
                  />
                  <Button
                    variant="outline"
                    onClick={() => assignMut.mutate()}
                    disabled={zone.trim() === '' || assignMut.isPending}
                  >
                    {t('servers.wizardAssignBtn')}
                  </Button>
                </div>
              </div>
            )}
          </div>
        )}

        <DialogFooter>
          {step === 1 ? (
            <>
              <Button variant="outline" onClick={() => onOpenChange(false)}>
                {t('common.cancel')}
              </Button>
              <Button onClick={onNext}>{t('servers.wizardNextBtn')}</Button>
            </>
          ) : (
            <>
              <Button variant="outline" onClick={() => setStep(1)}>
                {t('servers.wizardBackBtn')}
              </Button>
              <Button onClick={() => onOpenChange(false)}>{t('servers.wizardDoneBtn')}</Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
