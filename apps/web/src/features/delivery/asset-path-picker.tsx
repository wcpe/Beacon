// 交付域共用「文件路径 / 目录」选择控件：把原先手填路径的输入框换成级联可搜索下拉。
// 级联逻辑：已选定服务器时用 fetchAssets 拉该服真实清单（serverId + pathPrefix 服务端前缀过滤），
// 未选定服务器时退化为「整个 namespace 的清单」+ 常用目录兜底；任何情况都允许手输（allowCustom），
// 保证清单未覆盖的路径（新装插件 / 尚未上报扫描）仍能提交。
// 大清单保护（几千个文件的服不能卡死页面）：
//   1) 单次只拉一页（PAGE_SIZE）并缓存 30s，候选按页复用，不随每次渲染重复请求；
//   2) 键入值 debounce 后才进 queryKey，避免每敲一个字符打一次服务端；
//   3) 服务端前缀取「已输入内容的目录部分」——末段还在输入时前缀过窄会漏候选，
//      取到最后一个 '/' 才能既收窄查询范围、又让 Combobox 的前端子串过滤命中半截目录名；
//   4) 目录 / 文件候选去重后按上限截断，DOM 数量与清单规模解耦。
import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Combobox, type ComboboxOption } from '@beacon/ui'

import { fetchAssets } from '../../api/delivery-assets'

// 单次拉取的清单条数上限（服务端一页）
const PAGE_SIZE = 200
// 目录 / 文件候选条数上限：再多也没人往下翻，截断即保证大清单不卡
const MAX_OPTIONS = 50
// 键入防抖间隔（毫秒）
const TYPING_DEBOUNCE_MS = 300
// 未拿到清单时的常用目录兜底（路径是系统内的固定值，不做翻译）
const COMMON_DIRS = ['plugins/', 'mods/', 'config/', 'world/']
// 清单 queryKey 里 namespaceId 固定在第 3 位（见 assetPathQueryKey），
// 供 placeholderData 判断「上一批候选是否属于同一命名空间」
const NAMESPACE_KEY_INDEX = 2

/** 候选清单的 queryKey：namespaceId 位于索引 2，与 NAMESPACE_KEY_INDEX 必须保持一致 */
function assetPathQueryKey(namespaceId: number, serverId: string, directoryPrefix: string) {
  return ['delivery', 'asset-path-picker', namespaceId, serverId, directoryPrefix] as const
}

export type AssetPathMode = 'file' | 'directory'

export interface AssetPathPickerProps {
  namespaceId: number
  // 级联依据：给出已选服务器时只列该服真实路径；留空表示按整个 namespace 列候选
  serverId?: string
  value: string
  onChange: (path: string) => void
  // file：候选为完整文件路径（两侧 diff / 跨服比对）；directory：候选为目录前缀（扫描目录 / 路径前缀）
  mode?: AssetPathMode
  id?: string
  'aria-label'?: string
  placeholder?: string
  className?: string
  disabled?: boolean
  clearable?: boolean
}

export default function AssetPathPicker({
  namespaceId,
  serverId,
  value,
  onChange,
  mode = 'file',
  id,
  'aria-label': ariaLabel,
  placeholder,
  className,
  disabled,
  clearable,
}: AssetPathPickerProps) {
  const { t } = useTranslation()
  const debouncedValue = useDebouncedValue(value, TYPING_DEBOUNCE_MS)
  // 服务端前缀取已输入内容的目录部分，末段（可能只输了一半）交给前端子串过滤
  const directoryPrefix = directoryPrefixOf(debouncedValue)
  const scopedServerId = serverId === undefined || serverId === '' ? undefined : serverId

  const query = useQuery({
    // mode 只影响候选的聚合方式、不影响请求本身，故不进 queryKey：文件 / 目录两处可共享同一份清单缓存
    queryKey: assetPathQueryKey(namespaceId, scopedServerId ?? '', directoryPrefix),
    queryFn: () =>
      fetchAssets({
        namespaceId,
        serverId: scopedServerId,
        pathPrefix: directoryPrefix === '' ? undefined : directoryPrefix,
        pageSize: PAGE_SIZE,
      }),
    // namespace 尚未选定时不查（后端要求 namespaceId 必填，避免无索引全表扫描）
    enabled: namespaceId > 0,
    staleTime: 30_000,
    // 键入过程中沿用上一批候选，避免候选区闪烁清空。
    // 但 keepPreviousData 的语义是「key 变化即返回上一个 key 的 data」，而 namespaceId 也在 key 里：
    // 直接交给它会让切换命名空间后短暂展示上一个命名空间的路径候选。
    // 故显式比对上一批候选所属的 namespaceId——同一命名空间内（键入 / 翻目录）沿用旧候选，
    // 跨命名空间则返回 undefined，退回首次加载的空态（不清空、不残留别的命名空间的路径）。
    placeholderData: (previousData, previousQuery) =>
      previousQuery?.queryKey[NAMESPACE_KEY_INDEX] === namespaceId ? previousData : undefined,
  })

  const options = useMemo<ComboboxOption[]>(() => {
    const paths = (query.data?.items ?? []).map((item) => item.path)
    const candidates =
      mode === 'directory' ? toDirectoryPrefixes(paths, MAX_OPTIONS) : uniqueSortedPaths(paths, MAX_OPTIONS)
    // 目录模式把常用目录并入候选：清单还没到 / 该前缀下暂时没有文件时也有可选值
    const merged = mode === 'directory' ? mergeCommonDirs(candidates) : candidates
    return merged.map((path) => ({ value: path, label: path }))
  }, [query.data, mode])

  return (
    <Combobox
      id={id}
      aria-label={ariaLabel ?? placeholder ?? t('delivery.pickers.path.placeholder')}
      value={value}
      onChange={onChange}
      options={options}
      // 清单未覆盖的路径（新装插件 / 未上报扫描）仍必须能提交
      allowCustom
      placeholder={placeholder ?? t('delivery.pickers.path.placeholder')}
      emptyText={t('delivery.pickers.path.empty')}
      clearable={clearable}
      clearLabel={t('delivery.pickers.path.clear')}
      disabled={disabled}
      className={className}
    />
  )
}

/** 键入值防抖：保证快速输入时不会每个字符都触发一次清单查询 */
function useDebouncedValue<TValue>(value: TValue, delayMs: number): TValue {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebounced(value)
    }, delayMs)
    return () => {
      clearTimeout(timer)
    }
  }, [value, delayMs])
  return debounced
}

/** 取已输入内容的目录部分（含末尾 '/'）；无目录分隔符时返回空串表示「不限制前缀」 */
export function directoryPrefixOf(text: string): string {
  const trimmed = text.trim()
  const slash = trimmed.lastIndexOf('/')
  return slash < 0 ? '' : trimmed.slice(0, slash + 1)
}

/** 文件路径 → 逐层目录前缀（plugins/Essentials/config.yml → plugins/、plugins/Essentials/），去重 + 截断 */
export function toDirectoryPrefixes(paths: readonly string[], limit: number): string[] {
  const dirs = new Set<string>()
  for (const path of paths) {
    const segments = path.split('/')
    // 不含文件名本身（末段），也不含服根目录（空串）
    for (let depth = 1; depth < segments.length; depth += 1) {
      dirs.add(`${segments.slice(0, depth).join('/')}/`)
    }
    // 已远超上限就不必再聚合，避免超大清单反复切分字符串
    if (dirs.size > limit * 4) {
      break
    }
  }
  return [...dirs].sort((left, right) => left.localeCompare(right)).slice(0, limit)
}

/** 常用目录并入候选：去重后排序（真实目录已在上一步聚合，这里只补缺口） */
function mergeCommonDirs(dirs: readonly string[]): string[] {
  const merged = new Set(dirs)
  for (const dir of COMMON_DIRS) {
    merged.add(dir)
  }
  return [...merged].sort((left, right) => left.localeCompare(right)).slice(0, MAX_OPTIONS)
}

/** 文件路径去重 + 排序后截断 */
function uniqueSortedPaths(paths: readonly string[], limit: number): string[] {
  return [...new Set(paths)].sort((left, right) => left.localeCompare(right)).slice(0, limit)
}
