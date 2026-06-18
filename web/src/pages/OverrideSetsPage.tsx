// 三方文件覆盖兼容页（override-set，FR-15）：按 namespace/group 过滤列表。
// 选中进入详情：以 Sheet 右侧抽屉承载元数据 + 发布前 dry-run 只读预览（将覆盖哪些文件/执行什么命令 + AlertDialog 二次确认门控发布）+ 历史/回滚。
// 命令执行依赖鉴权且 agent 本地白名单放行，前端仅展示与确认，不做灰度向导（FR-9/P2 红线外）。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from 'react-router-dom'
import {
  dryRunOverrideSet,
  getOverrideSet,
  listOverrideSetRevisions,
  listOverrideSets,
  publishOverrideSet,
  rollbackOverrideSet,
} from '../api/client'
import type { OverrideSetFilter } from '../api/client'
import { formatTime } from '../api/format'
import { useMessage } from '../components/useMessage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'

export default function OverrideSetsPage() {
  const navigate = useNavigate()
  const { id } = useParams<{ id: string }>()
  const selectedId = id ? Number(id) : null

  // 过滤草稿与生效值（仅 namespace/group）
  const [fNamespace, setFNamespace] = useState('')
  const [fGroup, setFGroup] = useState('')
  const [filter, setFilter] = useState<OverrideSetFilter>({})

  const list = useQuery({
    queryKey: ['override-sets', filter],
    queryFn: () => listOverrideSets(filter),
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({ namespace: fNamespace.trim() || undefined, group: fGroup.trim() || undefined })
  }

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold">文件覆盖集</h1>

      <Card>
        <CardContent>
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="f-namespace">环境</Label>
              <Input id="f-namespace" value={fNamespace} onChange={(e) => setFNamespace(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-group">大区</Label>
              <Input id="f-group" value={fGroup} onChange={(e) => setFGroup(e.target.value)} />
            </div>
            <Button type="submit">查询</Button>
          </form>
        </CardContent>
      </Card>

      {list.isError && (
        <p className="text-sm text-destructive">加载失败：{(list.error as Error).message}</p>
      )}

      <Card>
        <CardContent>
          {list.isLoading ? (
            <p className="text-sm text-muted-foreground">加载中…</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>ID</TableHead>
                  <TableHead>名称</TableHead>
                  <TableHead>环境</TableHead>
                  <TableHead>大区</TableHead>
                  <TableHead>覆盖层</TableHead>
                  <TableHead>目标目录</TableHead>
                  <TableHead>重载命令</TableHead>
                  <TableHead>版本</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>更新时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.data && list.data.length > 0 ? (
                  list.data.map((o) => (
                    <TableRow
                      key={o.id}
                      className="cursor-pointer"
                      onClick={() => navigate(`/override-sets/${o.id}`)}
                    >
                      <TableCell>{o.id}</TableCell>
                      <TableCell>{o.name}</TableCell>
                      <TableCell>{o.namespace}</TableCell>
                      <TableCell>{o.group}</TableCell>
                      <TableCell>{o.scopeLevel}</TableCell>
                      <TableCell className="font-mono">{o.targetRoot}</TableCell>
                      <TableCell className="font-mono">{o.reloadCommand || '-'}</TableCell>
                      <TableCell>{o.version}</TableCell>
                      <TableCell>{o.enabled ? '启用' : '已删'}</TableCell>
                      <TableCell>{formatTime(o.updatedAt)}</TableCell>
                    </TableRow>
                  ))
                ) : (
                  <TableRow>
                    <TableCell colSpan={10} className="text-center text-muted-foreground">
                      无覆盖集
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* 详情 Sheet 由 URL 里有无 :id 控制：关闭即回列表路由 */}
      <Sheet
        open={selectedId !== null}
        onOpenChange={(open) => {
          if (!open) navigate('/override-sets')
        }}
      >
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-2xl">
          {selectedId !== null && <OverrideSetDetail key={selectedId} id={selectedId} />}
        </SheetContent>
      </Sheet>
    </div>
  )
}

// 覆盖集详情：元数据 + dry-run 只读预览（高危覆盖安全闸）+ AlertDialog 二次确认门控发布 + 历史/回滚。
function OverrideSetDetail({ id }: { id: number }) {
  const qc = useQueryClient()
  const msg = useMessage()

  // 发布表单
  const [targetRoot, setTargetRoot] = useState('')
  const [reloadCommand, setReloadCommand] = useState('')
  const [comment, setComment] = useState('')
  // 发布二次确认弹窗开关
  const [confirmOpen, setConfirmOpen] = useState(false)

  const detail = useQuery({ queryKey: ['override-set', id], queryFn: () => getOverrideSet(id) })
  const revisions = useQuery({
    queryKey: ['override-set-revisions', id],
    queryFn: () => listOverrideSetRevisions(id),
  })
  const dryRun = useQuery({ queryKey: ['override-set-dryrun', id], queryFn: () => dryRunOverrideSet(id) })

  function invalidateAll() {
    qc.invalidateQueries({ queryKey: ['override-set', id] })
    qc.invalidateQueries({ queryKey: ['override-set-revisions', id] })
    qc.invalidateQueries({ queryKey: ['override-set-dryrun', id] })
    qc.invalidateQueries({ queryKey: ['override-sets'] })
  }

  const publishMut = useMutation({
    mutationFn: () => publishOverrideSet(id, targetRoot.trim(), reloadCommand.trim(), comment.trim()),
    onSuccess: (r) => {
      msg.showSuccess(`已发布覆盖集版本 ${r.version}（目标 ${r.targetRoot}）`)
      setComment('')
      invalidateAll()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const rollbackMut = useMutation({
    mutationFn: (toVersion: number) => rollbackOverrideSet(id, toVersion, `回滚到版本 ${toVersion}`),
    onSuccess: (r) => {
      msg.showSuccess(`已回滚，新版本 ${r.version}`)
      invalidateAll()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 把当前覆盖集的目标目录/重载命令载入发布表单，便于在此基础上改。
  function fillFromCurrent() {
    if (detail.data) {
      setTargetRoot(detail.data.targetRoot)
      setReloadCommand(detail.data.reloadCommand)
    }
  }

  // 点「发布」：先做目标目录非空校验，通过才弹二次确认。
  function onPublishClick() {
    if (!targetRoot.trim()) {
      msg.showError('目标目录不能为空')
      return
    }
    setConfirmOpen(true)
  }

  return (
    <>
      <SheetHeader className="px-0 pt-0">
        <SheetTitle>覆盖集详情 #{id}</SheetTitle>
      </SheetHeader>

      <div className="space-y-6">
        {detail.isError && (
          <p className="text-sm text-destructive">加载失败：{(detail.error as Error).message}</p>
        )}
        {detail.data && (
          <Card>
            <CardContent>
              <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
                <dt className="text-muted-foreground">名称</dt>
                <dd>{detail.data.name}</dd>
                <dt className="text-muted-foreground">环境</dt>
                <dd>{detail.data.namespace}</dd>
                <dt className="text-muted-foreground">大区</dt>
                <dd>{detail.data.group}</dd>
                <dt className="text-muted-foreground">覆盖层</dt>
                <dd>
                  {detail.data.scopeLevel}
                  {detail.data.scopeTarget ? ` / ${detail.data.scopeTarget}` : ''}
                </dd>
                <dt className="text-muted-foreground">目标目录</dt>
                <dd className="font-mono">{detail.data.targetRoot}</dd>
                <dt className="text-muted-foreground">重载命令</dt>
                <dd className="font-mono">{detail.data.reloadCommand || '（无）'}</dd>
                <dt className="text-muted-foreground">当前版本</dt>
                <dd>{detail.data.version}</dd>
                <dt className="text-muted-foreground">状态</dt>
                <dd>{detail.data.enabled ? '启用' : '已删'}</dd>
              </dl>
            </CardContent>
          </Card>
        )}

        {/* 发布前 dry-run 只读预览（高危覆盖安全闸，进入即自动加载） */}
        <div className="space-y-2">
          <h3 className="text-sm font-medium">发布前 dry-run 预览（高危覆盖安全闸）</h3>
          {dryRun.isError && (
            <p className="text-sm text-destructive">预览失败：{(dryRun.error as Error).message}</p>
          )}
          {dryRun.isFetching && <p className="text-sm text-muted-foreground">预览中…</p>}
          {dryRun.data && (
            <Card>
              <CardContent className="space-y-3 text-sm">
                <p>
                  将向目标目录 <span className="font-mono">{dryRun.data.targetRoot}</span> 覆盖以下{' '}
                  {dryRun.data.memberPaths.length} 个文件：
                </p>
                <ul className="space-y-1 font-mono text-xs">
                  {dryRun.data.memberPaths.length > 0 ? (
                    dryRun.data.memberPaths.map((p) => <li key={p}>{p}</li>)
                  ) : (
                    <li>（无成员文件）</li>
                  )}
                </ul>
                <p>
                  覆盖后执行重载命令：
                  <span className="font-mono">{dryRun.data.reloadCommand || '（无）'}</span>
                  （首 token <span className="font-mono">{dryRun.data.commandFirstToken || '-'}</span>
                  ，须在 agent 本地白名单内才会执行）
                </p>
              </CardContent>
            </Card>
          )}
        </div>

        {/* 发布新版本：去掉勾选门控，改为「发布」按钮触发 AlertDialog 二次确认 */}
        <div className="space-y-3">
          <h3 className="text-sm font-medium">发布新版本</h3>
          <Button type="button" variant="link" className="h-auto p-0" onClick={fillFromCurrent}>
            载入当前目标/命令到表单
          </Button>
          <div className="space-y-1.5">
            <Label htmlFor="os-target">目标插件目录（plugins/&lt;plugin&gt; 内）</Label>
            <Input
              id="os-target"
              value={targetRoot}
              onChange={(e) => setTargetRoot(e.target.value)}
              placeholder="如 plugins/DeluxeMenus"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="os-reload">重载命令（单条控制台命令，须在 agent 本地白名单内）</Label>
            <Input
              id="os-reload"
              value={reloadCommand}
              onChange={(e) => setReloadCommand(e.target.value)}
              placeholder="如 deluxemenus reload"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="os-comment">变更备注（可选）</Label>
            <Input id="os-comment" value={comment} onChange={(e) => setComment(e.target.value)} />
          </div>
          <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
            <Button type="button" disabled={publishMut.isPending} onClick={onPublishClick}>
              发布
            </Button>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>确认发布该覆盖集？</AlertDialogTitle>
                <AlertDialogDescription>
                  将按目标目录 {targetRoot.trim()} 覆盖 dry-run 预览所列文件，并执行重载命令
                  {`<${dryRun.data?.commandFirstToken || '-'}…>`}。
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>取消</AlertDialogCancel>
                <AlertDialogAction onClick={() => publishMut.mutate()}>确认发布</AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>

        {/* 历史版本 + 回滚 AlertDialog */}
        <div className="space-y-2">
          <h3 className="text-sm font-medium">历史版本</h3>
          {revisions.isError && (
            <p className="text-sm text-destructive">加载失败：{(revisions.error as Error).message}</p>
          )}
          {revisions.isLoading ? (
            <p className="text-sm text-muted-foreground">加载中…</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>版本</TableHead>
                  <TableHead>目标目录</TableHead>
                  <TableHead>重载命令</TableHead>
                  <TableHead>操作人</TableHead>
                  <TableHead>备注</TableHead>
                  <TableHead>创建时间</TableHead>
                  <TableHead>操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {revisions.data && revisions.data.length > 0 ? (
                  revisions.data.map((rev) => (
                    <TableRow key={rev.version}>
                      <TableCell>{rev.version}</TableCell>
                      <TableCell className="font-mono">{rev.targetRoot}</TableCell>
                      <TableCell className="font-mono">{rev.reloadCommand || '-'}</TableCell>
                      <TableCell>{rev.operator}</TableCell>
                      <TableCell>{rev.comment || '-'}</TableCell>
                      <TableCell>{formatTime(rev.createdAt)}</TableCell>
                      <TableCell>
                        <AlertDialog>
                          <AlertDialogTrigger asChild>
                            <Button variant="outline" size="sm" disabled={rollbackMut.isPending}>
                              回滚到此
                            </Button>
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>确认回滚覆盖集到版本 {rev.version}？</AlertDialogTitle>
                              <AlertDialogDescription>将作为新版本发布。</AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>取消</AlertDialogCancel>
                              <AlertDialogAction onClick={() => rollbackMut.mutate(rev.version)}>
                                确认回滚
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      </TableCell>
                    </TableRow>
                  ))
                ) : (
                  <TableRow>
                    <TableCell colSpan={7} className="text-center text-muted-foreground">
                      无历史版本
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </div>
      </div>
    </>
  )
}
