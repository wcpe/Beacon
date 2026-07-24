// env 页（/envs，FR-178）：主从布局——左主列（吸顶搜索 + 创建 + 自区滚列表 + 吸底分页），
// 点行 → 右侧非模态详情面板（该 env 概要 + 映射的 namespace + 编辑 / 设置映射 / 删除入口）。
// env 是纯展示 / 过滤维度：增删改与整体替换 env→namespace 映射均只影响顶栏过滤视图，不动任何权威数据。
// 一个 namespace 至多属于一个 env——设置映射时被其他 env 占用者由后端 409 拒绝并指明冲突方（脱敏文案内联展示）。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Layers, Search } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  DestructiveConfirmDialog,
  Input,
  PageHeader,
  TableSkeleton,
  type DataTableColumn,
} from '@beacon/ui'
import type { EnvItem } from '@beacon/contracts'

import {
  ApiClientError,
  createEnv,
  deleteEnv,
  fetchEnvList,
  setEnvNamespaces,
  updateEnv,
} from '../api/system'
import { fetchNamespaceList } from '../api/system'
import { formatIso } from '../features/system/format'
import ListCard from '../features/shared/list-card'
import MasterDetail from '../features/shared/master-detail'
import Pager from '../features/observability/pager'
import EnvDetailPanel from './envs/env-detail-panel'
import EnvFormDialog from './envs/env-form-dialog'
import MappingDialog from './envs/mapping-dialog'

const PAGE_SIZE = 15

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

// 表单态：创建（空）或编辑（带当前 env）；null = 关闭
type FormState = { mode: 'create' } | { mode: 'edit'; env: EnvItem } | null

export default function EnvsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  const [selectedId, setSelectedId] = useState<number | null>(null)

  const [formState, setFormState] = useState<FormState>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [mappingEnv, setMappingEnv] = useState<EnvItem | null>(null)
  const [mappingError, setMappingError] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<EnvItem | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['envs', 'list', keyword, page],
    queryFn: () =>
      fetchEnvList({
        keyword: keyword.trim() === '' ? undefined : keyword.trim(),
        page,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  })

  // 全量 env（供映射编辑标注占用方）——与顶栏过滤器同一 query key，避免重复请求
  const envOptionsQuery = useQuery({
    queryKey: ['envs', 'options'],
    queryFn: () => fetchEnvList({ pageSize: 100 }),
  })

  // 全量 namespace（映射编辑的候选）
  const namespaceOptionsQuery = useQuery({
    queryKey: ['namespaces', 'options'],
    queryFn: () => fetchNamespaceList({ pageSize: 100 }),
  })

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const items = useMemo(() => query.data?.items ?? [], [query.data])
  const selected = useMemo(() => items.find((row) => row.id === selectedId) ?? null, [items, selectedId])

  // env 写操作后统一失效 ['envs']（覆盖列表 / 选项 / 顶栏过滤器与 NamespaceSelect 的 env 作用域）
  const invalidateEnvs = async () => {
    await queryClient.invalidateQueries({ queryKey: ['envs'] })
  }

  const createMutation = useMutation({
    mutationFn: ({ name, description }: { name: string; description: string }) =>
      createEnv({ name, description: description || undefined }),
    onSuccess: async () => {
      await invalidateEnvs()
      setFormState(null)
    },
    onError: (error) => {
      setFormError(messageOf(error))
    },
  })

  const updateMutation = useMutation({
    mutationFn: ({ id, name, description }: { id: number; name: string; description: string }) =>
      updateEnv(id, { name, description }),
    onSuccess: async () => {
      await invalidateEnvs()
      setFormState(null)
    },
    onError: (error) => {
      setFormError(messageOf(error))
    },
  })

  const mappingMutation = useMutation({
    mutationFn: ({ id, namespaceIds }: { id: number; namespaceIds: number[] }) => setEnvNamespaces(id, namespaceIds),
    onSuccess: async () => {
      await invalidateEnvs()
      setMappingEnv(null)
    },
    onError: (error) => {
      setMappingError(messageOf(error))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => deleteEnv(id),
    onSuccess: async () => {
      await invalidateEnvs()
      setDeleting(null)
      setSelectedId(null)
    },
    onError: (error) => {
      setDeleteError(messageOf(error))
      setDeleting(null)
    },
  })

  const columns = useMemo<DataTableColumn<EnvItem>[]>(
    () => [
      { header: t('system.envs.columns.name'), cell: (row) => <span className="font-medium">{row.name}</span> },
      { header: t('system.envs.columns.description'), cell: (row) => row.description || '-' },
      {
        header: t('system.envs.columns.namespaceCount'),
        cell: (row) => (
          <Badge variant={row.namespaceCount > 0 ? 'brand' : 'secondary'}>
            {t('system.envs.namespaceCountLabel', { count: row.namespaceCount })}
          </Badge>
        ),
      },
      { header: t('system.envs.columns.updatedAt'), cell: (row) => formatIso(row.updatedAt) },
    ],
    [t],
  )

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <Layers className="size-[15px]" />
          </span>
          {t('system.envs.listTitle')}
        </span>
        {total > 0 && <span className="text-xs text-ink-3">{t('system.envs.total', { count: total })}</span>}
        <Button
          className="ml-auto"
          onClick={() => {
            setFormError(null)
            setFormState({ mode: 'create' })
          }}
        >
          {t('system.envs.create')}
        </Button>
      </div>
      <div className="relative w-64 max-w-full">
        <Search
          className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-ink-4"
          aria-hidden
        />
        <Input
          aria-label={t('system.envs.keyword')}
          placeholder={t('system.envs.keyword')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
          className="pl-8"
        />
      </div>
    </div>
  )

  const master = (
    <ListCard
      toolbar={toolbar}
      footer={
        total > PAGE_SIZE ? <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} /> : undefined
      }
    >
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={columns.length} rows={6} />}
      >
        <DataTable
          columns={columns}
          rows={items}
          rowKey={(row) => String(row.id)}
          emptyText={t('system.envs.empty')}
          density="compact"
          onRowClick={(row) => {
            setSelectedId(row.id)
          }}
          rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50/60' : undefined)}
        />
      </AsyncSection>
    </ListCard>
  )

  return (
    <section className="grid gap-4">
      <PageHeader icon={<Layers className="size-5" />} title={t('nav.envs')} />
      {/* env 定位提示：品牌浅底，突出「纯展示 / 过滤维度、不动权威数据」 */}
      <div className="flex items-start gap-2.5 rounded-xl border border-brand-100 bg-brand-50 px-4 py-3">
        <Layers className="mt-0.5 size-4 shrink-0 text-brand" aria-hidden />
        <p className="text-sm text-ink-2">{t('system.envs.hint')}</p>
      </div>

      {deleteError && <p className="text-sm text-destructive">{deleteError}</p>}

      <MasterDetail
        master={master}
        detail={
          selected ? (
            <EnvDetailPanel
              env={selected}
              onEdit={() => {
                setFormError(null)
                setFormState({ mode: 'edit', env: selected })
              }}
              onSetMapping={() => {
                setMappingError(null)
                setMappingEnv(selected)
              }}
              onDelete={() => {
                setDeleteError(null)
                setDeleting(selected)
              }}
            />
          ) : null
        }
        detailTitle={t('system.envs.detailTitle')}
        closeLabel={t('system.common.close')}
        onClose={() => {
          setSelectedId(null)
        }}
      />

      {/* 创建 / 编辑 env */}
      <EnvFormDialog
        open={formState !== null}
        mode={formState?.mode ?? 'create'}
        initialName={formState?.mode === 'edit' ? formState.env.name : ''}
        initialDescription={formState?.mode === 'edit' ? formState.env.description : ''}
        pending={createMutation.isPending || updateMutation.isPending}
        errorText={formError}
        onOpenChange={(open) => {
          if (!open) {
            setFormState(null)
          }
        }}
        onSubmit={(name, description) => {
          setFormError(null)
          if (formState?.mode === 'edit') {
            updateMutation.mutate({ id: formState.env.id, name, description })
          } else {
            createMutation.mutate({ name, description })
          }
        }}
      />

      {/* 设置 env→namespace 映射（整体替换，冲突 409 指明冲突方） */}
      <MappingDialog
        open={mappingEnv !== null}
        env={mappingEnv}
        namespaces={namespaceOptionsQuery.data?.items ?? []}
        allEnvs={envOptionsQuery.data?.items ?? []}
        pending={mappingMutation.isPending}
        errorText={mappingError}
        onOpenChange={(open) => {
          if (!open) {
            setMappingEnv(null)
          }
        }}
        onSubmit={(namespaceIds) => {
          if (mappingEnv) {
            setMappingError(null)
            mappingMutation.mutate({ id: mappingEnv.id, namespaceIds })
          }
        }}
      />

      {/* 删除 env（映射级联删除，仅影响过滤视图） */}
      <DestructiveConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleting(null)
          }
        }}
        title={t('system.envs.confirmDeleteTitle', { name: deleting?.name ?? '' })}
        description={t('system.envs.confirmDeleteDesc')}
        confirmLabel={t('system.envs.confirmDelete')}
        cancelLabel={t('system.common.cancel')}
        impacts={
          deleting && deleting.namespaceCount > 0
            ? [t('system.envs.namespaceCountLabel', { count: deleting.namespaceCount })]
            : undefined
        }
        pending={deleteMutation.isPending}
        onConfirm={() => {
          if (deleting) {
            deleteMutation.mutate(deleting.id)
          }
        }}
      />
    </section>
  )
}
