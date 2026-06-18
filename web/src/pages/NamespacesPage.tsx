// 环境管理页：列出环境（namespace）+ 新建。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createNamespace, listNamespaces } from '../api/client'
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
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'

export default function NamespacesPage() {
  const qc = useQueryClient()
  const msg = useMessage()
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  // 新建环境 Dialog 开关
  const [createOpen, setCreateOpen] = useState(false)

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['namespaces'],
    queryFn: listNamespaces,
  })

  const createMut = useMutation({
    mutationFn: () => createNamespace(code.trim(), name.trim()),
    onSuccess: (ns) => {
      msg.showSuccess(`已新建环境 ${ns.code}`)
      setCode('')
      setName('')
      setCreateOpen(false)
      qc.invalidateQueries({ queryKey: ['namespaces'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!code.trim() || !name.trim()) {
      msg.showError('环境编码与名称均为必填')
      return
    }
    createMut.mutate()
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">环境管理</h1>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button>新建环境</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>新建环境</DialogTitle>
            </DialogHeader>
            <form id="create-namespace" onSubmit={onCreate} className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="n-code">编码</Label>
                <Input
                  id="n-code"
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  placeholder="如 prod"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="n-name">名称</Label>
                <Input
                  id="n-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="如 生产环境"
                />
              </div>
            </form>
            <DialogFooter>
              <Button type="submit" form="create-namespace" disabled={createMut.isPending}>
                新建
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {isError && (
        <p className="text-sm text-destructive">加载失败：{(error as Error).message}</p>
      )}

      <Card>
        <CardContent>
          {isLoading ? (
            <p className="text-sm text-muted-foreground">加载中…</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>编码</TableHead>
                  <TableHead>名称</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data && data.length > 0 ? (
                  data.map((ns) => (
                    <TableRow key={ns.code}>
                      <TableCell>{ns.code}</TableCell>
                      <TableCell>{ns.name}</TableCell>
                    </TableRow>
                  ))
                ) : (
                  <TableRow>
                    <TableCell colSpan={2} className="text-center text-muted-foreground">
                      暂无环境
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
