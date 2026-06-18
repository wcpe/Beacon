// 文件详情独立路由页（/files/:id，通道B，FR-14）：概览 / 发布 / 历史 / 对比 四个 Tab。
// 发布、回滚、软删行为与 API 与改造前一致；回滚/软删的确认由 window.confirm 换为 AlertDialog。
// 文件无 /diff 端点，对比取两个历史版本整文件内容并排展示（前端不算差异、不合并，仅并列原文）。

import { useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import {
  deleteFile,
  getFile,
  getFileRevision,
  listFileRevisions,
  publishFile,
  rollbackFile,
} from '../api/client'
import { formatTime } from '../api/format'
import CodeEditor from '../components/CodeEditor'
import { useMessage } from '../components/useMessage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
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

export default function FileDetailPage() {
  const qc = useQueryClient()
  const msg = useMessage()
  const navigate = useNavigate()
  const { id: idParam } = useParams<{ id: string }>()
  const id = Number(idParam)

  // 发布表单
  const [content, setContent] = useState('')
  const [comment, setComment] = useState('')
  // diff 选择的两个版本
  const [diffFrom, setDiffFrom] = useState<number | ''>('')
  const [diffTo, setDiffTo] = useState<number | ''>('')

  const detail = useQuery({
    queryKey: ['file', id],
    queryFn: () => getFile(id),
  })

  const revisions = useQuery({
    queryKey: ['file-revisions', id],
    queryFn: () => listFileRevisions(id),
  })

  // 并排 diff：分别取两个版本的整文件内容（文件无 /diff 端点）
  const diffPair = useQueries({
    queries: [
      {
        queryKey: ['file-revision', id, diffFrom],
        queryFn: () => getFileRevision(id, Number(diffFrom)),
        enabled: diffFrom !== '',
      },
      {
        queryKey: ['file-revision', id, diffTo],
        queryFn: () => getFileRevision(id, Number(diffTo)),
        enabled: diffTo !== '',
      },
    ],
  })
  const [fromRev, toRev] = diffPair
  const diffReady = diffFrom !== '' && diffTo !== '' && fromRev.data && toRev.data
  const diffLoading = (diffFrom !== '' && fromRev.isFetching) || (diffTo !== '' && toRev.isFetching)
  const diffError = fromRev.error || toRev.error

  function invalidateAll() {
    qc.invalidateQueries({ queryKey: ['file', id] })
    qc.invalidateQueries({ queryKey: ['file-revisions', id] })
    qc.invalidateQueries({ queryKey: ['files'] })
  }

  const publishMut = useMutation({
    mutationFn: () => publishFile(id, content, comment.trim()),
    onSuccess: (r) => {
      msg.showSuccess(`已发布版本 ${r.version}（md5 ${r.md5.slice(0, 8)}）`)
      setComment('')
      invalidateAll()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const rollbackMut = useMutation({
    mutationFn: (toVersion: number) => rollbackFile(id, toVersion, `回滚到版本 ${toVersion}`),
    onSuccess: (r) => {
      msg.showSuccess(`已回滚，新版本 ${r.version}`)
      invalidateAll()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: () => deleteFile(id, '管理台软删'),
    onSuccess: () => {
      msg.showSuccess('已软删该文件层')
      qc.invalidateQueries({ queryKey: ['files'] })
      navigate('/files')
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onPublish(e: React.FormEvent) {
    e.preventDefault()
    if (!content) {
      msg.showError('发布内容不能为空')
      return
    }
    publishMut.mutate()
  }

  // 进入详情后把当前内容填入发布框，便于在此基础上改
  function fillFromCurrent() {
    if (detail.data?.content !== undefined) setContent(detail.data.content)
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Button variant="outline" size="sm" onClick={() => navigate('/files')}>
          <ArrowLeft />
          返回
        </Button>
        <h1 className="text-xl font-semibold">文件详情 #{id}</h1>
      </div>

      {detail.isError && (
        <p className="text-sm text-destructive">加载失败：{(detail.error as Error).message}</p>
      )}
      {detail.isLoading && <p className="text-sm text-muted-foreground">加载中…</p>}

      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">概览</TabsTrigger>
          <TabsTrigger value="publish">发布</TabsTrigger>
          <TabsTrigger value="history">历史</TabsTrigger>
          <TabsTrigger value="diff">对比</TabsTrigger>
        </TabsList>

        {/* 概览：元数据 + 当前内容 */}
        <TabsContent value="overview" className="space-y-4">
          {detail.data && (
            <Card>
              <CardContent className="space-y-4">
                <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
                  <dt className="text-muted-foreground">环境</dt>
                  <dd>{detail.data.namespace}</dd>
                  <dt className="text-muted-foreground">大区</dt>
                  <dd>{detail.data.group}</dd>
                  <dt className="text-muted-foreground">路径</dt>
                  <dd className="font-mono">{detail.data.path}</dd>
                  <dt className="text-muted-foreground">覆盖层</dt>
                  <dd>
                    {detail.data.scopeLevel}
                    {detail.data.scopeTarget ? ` / ${detail.data.scopeTarget}` : ''}
                  </dd>
                  <dt className="text-muted-foreground">当前版本</dt>
                  <dd>{detail.data.version}</dd>
                  <dt className="text-muted-foreground">md5</dt>
                  <dd className="font-mono">{detail.data.md5}</dd>
                  <dt className="text-muted-foreground">启用</dt>
                  <dd>{detail.data.enabled ? '是' : '否（已软删）'}</dd>
                  <dt className="text-muted-foreground">更新时间</dt>
                  <dd>{formatTime(detail.data.updatedAt)}</dd>
                </dl>
                <div>
                  <div className="mb-1.5 text-sm font-medium">当前内容</div>
                  <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-3 font-mono text-xs">
                    {detail.data.content}
                  </pre>
                </div>
              </CardContent>
            </Card>
          )}
        </TabsContent>

        {/* 发布：载入当前内容 + 编辑 + 发布 / 软删 */}
        <TabsContent value="publish">
          <Card>
            <CardContent>
              <form onSubmit={onPublish} className="space-y-3">
                <Button type="button" variant="link" className="h-auto p-0" onClick={fillFromCurrent}>
                  载入当前内容到编辑框
                </Button>
                <CodeEditor
                  value={content}
                  onChange={setContent}
                />
                <div className="space-y-1.5">
                  <Label htmlFor="publish-comment">变更备注（可选）</Label>
                  <Input
                    id="publish-comment"
                    className="max-w-md"
                    value={comment}
                    onChange={(e) => setComment(e.target.value)}
                  />
                </div>
                <div className="flex gap-2">
                  <Button type="submit" disabled={publishMut.isPending}>
                    发布
                  </Button>
                  <AlertDialog>
                    <AlertDialogTrigger asChild>
                      <Button type="button" variant="destructive" disabled={deleteMut.isPending}>
                        软删此层
                      </Button>
                    </AlertDialogTrigger>
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>确认软删该文件层？</AlertDialogTitle>
                        <AlertDialogDescription>
                          该层将从覆盖链脱落，下游 agent 据 manifest 删除镜像。
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>取消</AlertDialogCancel>
                        <AlertDialogAction onClick={() => deleteMut.mutate()}>确认软删</AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                </div>
              </form>
            </CardContent>
          </Card>
        </TabsContent>

        {/* 历史：版本列表 + 回滚 */}
        <TabsContent value="history">
          <Card>
            <CardContent>
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
                      <TableHead>md5</TableHead>
                      <TableHead>操作人</TableHead>
                      <TableHead>备注</TableHead>
                      <TableHead>来源版本</TableHead>
                      <TableHead>创建时间</TableHead>
                      <TableHead>操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {revisions.data && revisions.data.length > 0 ? (
                      revisions.data.map((rev) => (
                        <TableRow key={rev.version}>
                          <TableCell>{rev.version}</TableCell>
                          <TableCell className="font-mono">{rev.md5.slice(0, 8)}</TableCell>
                          <TableCell>{rev.operator}</TableCell>
                          <TableCell>{rev.comment || '-'}</TableCell>
                          <TableCell>{rev.sourceRevision ?? '-'}</TableCell>
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
                                  <AlertDialogTitle>确认回滚到版本 {rev.version}？</AlertDialogTitle>
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
            </CardContent>
          </Card>
        </TabsContent>

        {/* 对比：选择两个版本，分别取整文件内容并排展示（文件无 /diff 端点） */}
        <TabsContent value="diff">
          <Card>
            <CardContent className="space-y-4">
              <div className="flex flex-wrap gap-4">
                <div className="space-y-1.5">
                  <Label>旧版本</Label>
                  <Select
                    value={diffFrom === '' ? undefined : String(diffFrom)}
                    onValueChange={(v) => setDiffFrom(Number(v))}
                  >
                    <SelectTrigger className="w-32">
                      <SelectValue placeholder="选择" />
                    </SelectTrigger>
                    <SelectContent>
                      {revisions.data?.map((rev) => (
                        <SelectItem key={rev.version} value={String(rev.version)}>
                          v{rev.version}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-1.5">
                  <Label>新版本</Label>
                  <Select
                    value={diffTo === '' ? undefined : String(diffTo)}
                    onValueChange={(v) => setDiffTo(Number(v))}
                  >
                    <SelectTrigger className="w-32">
                      <SelectValue placeholder="选择" />
                    </SelectTrigger>
                    <SelectContent>
                      {revisions.data?.map((rev) => (
                        <SelectItem key={rev.version} value={String(rev.version)}>
                          v{rev.version}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              {diffError && (
                <p className="text-sm text-destructive">对比失败：{(diffError as Error).message}</p>
              )}
              {diffLoading && <p className="text-sm text-muted-foreground">对比中…</p>}
              {diffReady && (
                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <div className="mb-1 text-sm font-medium">v{fromRev.data!.version}</div>
                    <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-3 font-mono text-xs">
                      {fromRev.data!.content}
                    </pre>
                  </div>
                  <div>
                    <div className="mb-1 text-sm font-medium">v{toRev.data!.version}</div>
                    <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-3 font-mono text-xs">
                      {toRev.data!.content}
                    </pre>
                  </div>
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  )
}
