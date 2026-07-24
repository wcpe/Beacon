// 站内开源协议页（FR-190）：
// 1) 项目自身 MIT 完整正文（与仓库根 LICENSE 一致）
// 2) 运行时第三方依赖清单（npm 管理台产物链 + Go 控制面），可搜索、可展开
// 清单数据由 apps/web/src/data/third-party-licenses.json 预生成，构建期内嵌。
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, ExternalLink, Search, Scale } from 'lucide-react'

import { Badge, Input, PageHeader } from '@beacon/ui'

import licenseData from '../data/third-party-licenses.json'

/** 与仓库根 LICENSE 字节级一致的 MIT 正文。 */
export const MIT_LICENSE_TEXT = `MIT License

Copyright (c) 2026 wcpe

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`

const REPO_LICENSE_URL = 'https://github.com/wcpe/Beacon/blob/master/LICENSE'

interface DepItem {
  name: string
  version: string
  license: string
  author: string
  homepage: string
  ecosystem: string
}

interface DepGroup {
  id: string
  titleKey: string
  items: DepItem[]
}

interface LicenseInventory {
  generatedAt: string
  project: {
    name: string
    license: string
    copyright: string
    spdx: string
    repository: string
  }
  groups: DepGroup[]
  counts: { npm: number; go: number; total: number }
}

const inventory = licenseData as LicenseInventory

function licenseBadgeVariant(license: string): 'brand' | 'secondary' | 'ok' | 'warn' {
  const u = license.toUpperCase()
  if (u.includes('MIT')) return 'brand'
  if (u.includes('APACHE')) return 'ok'
  if (u.includes('BSD')) return 'secondary'
  if (u.includes('GPL')) return 'warn'
  return 'secondary'
}

export default function LicensePage() {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [expanded, setExpanded] = useState<string | null>(null)

  const filteredGroups = useMemo(() => {
    const q = query.trim().toLowerCase()
    return inventory.groups.map((group) => {
      const items =
        q === ''
          ? group.items
          : group.items.filter((item) => {
              const hay = `${item.name} ${item.version} ${item.license} ${item.author}`.toLowerCase()
              return hay.includes(q)
            })
      return { ...group, items }
    })
  }, [query])

  const visibleTotal = filteredGroups.reduce((n, g) => n + g.items.length, 0)

  return (
    <section className="grid max-w-5xl gap-6" data-slot="license-page">
      <PageHeader
        icon={<Scale className="size-5" />}
        title={t('common.license.pageTitle')}
        description={t('common.license.intro')}
      />

      {/* 搜索 + 运行时依赖表（参考截图：单一清单） */}
      <div className="grid gap-3" data-slot="license-deps">
        <div className="relative">
          <Search
            className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-ink-4"
            aria-hidden
          />
          <Input
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
            }}
            placeholder={t('common.license.searchPlaceholder')}
            aria-label={t('common.license.searchPlaceholder')}
            className="h-10 pl-9"
            data-slot="license-search"
          />
        </div>

        {visibleTotal === 0 ? (
          <p className="rounded-xl border border-border bg-card px-4 py-8 text-center text-[13px] text-ink-4">
            {t('common.license.noMatch')}
          </p>
        ) : (
          filteredGroups.map((group) =>
            group.items.length === 0 ? null : (
              <DepTable
                key={group.id}
                title={t(group.titleKey, { count: group.items.length })}
                items={group.items}
                expanded={expanded}
                onToggle={(name) => {
                  setExpanded((cur) => (cur === name ? null : name))
                }}
              />
            ),
          )
        )}
      </div>

      {/* 项目自身 MIT 全文（折叠感：放在依赖表下方，避免喧宾夺主） */}
      <div className="grid gap-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-[13px] font-semibold tracking-wide text-ink-1 uppercase">
            {t('common.license.fullTextTitle')}
          </h2>
          <div className="flex flex-wrap items-center gap-2 text-[12px] text-ink-4">
            <Badge variant="brand">{inventory.project.spdx}</Badge>
            <span>{inventory.project.copyright}</span>
            <a
              href={REPO_LICENSE_URL}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 hover:text-ink-2 hover:underline"
            >
              {t('common.license.viewOnGithub')}
              <ExternalLink className="size-3.5" aria-hidden />
            </a>
          </div>
        </div>
        <pre
          data-slot="license-full-text"
          className="max-h-[240px] overflow-auto rounded-xl border border-border bg-card p-5 font-mono text-[12.5px] leading-[1.7] whitespace-pre-wrap text-ink-2 shadow-card"
        >
          {MIT_LICENSE_TEXT}
        </pre>
      </div>
    </section>
  )
}

function DepTable({
  title,
  items,
  expanded,
  onToggle,
}: {
  title: string
  items: DepItem[]
  expanded: string | null
  onToggle: (name: string) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="overflow-hidden rounded-xl border border-border bg-card shadow-card">
      <div className="border-b border-border px-4 py-3 text-[14px] font-semibold text-ink-1">
        {title}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[640px] border-collapse text-left text-[13px]">
          <thead>
            <tr className="border-b border-border text-[12px] text-ink-4">
              <th className="px-4 py-2.5 font-medium">{t('common.license.colPackage')}</th>
              <th className="px-3 py-2.5 font-medium">{t('common.license.colVersion')}</th>
              <th className="px-3 py-2.5 font-medium">{t('common.license.colLicense')}</th>
              <th className="px-3 py-2.5 font-medium">{t('common.license.colAuthor')}</th>
            </tr>
          </thead>
          <tbody>
            {items.map((item, index) => {
              const open = expanded === item.name
              return (
                <tr
                  key={`${item.name}@${item.version}`}
                  className={index % 2 === 0 ? 'bg-card' : 'bg-surface-2/50'}
                >
                  <td className="px-4 py-2.5 align-top">
                    <button
                      type="button"
                      className="inline-flex max-w-full items-start gap-1.5 text-left text-brand hover:underline"
                      onClick={() => {
                        onToggle(item.name)
                      }}
                      aria-expanded={open}
                    >
                      {open ? (
                        <ChevronDown className="mt-0.5 size-3.5 shrink-0" aria-hidden />
                      ) : (
                        <ChevronRight className="mt-0.5 size-3.5 shrink-0" aria-hidden />
                      )}
                      <span className="min-w-0 break-all">{item.name}</span>
                    </button>
                    {open ? (
                      <div className="mt-2 ml-5 grid gap-1 text-[12px] text-ink-3">
                        {item.homepage ? (
                          <a
                            href={item.homepage}
                            target="_blank"
                            rel="noreferrer"
                            className="inline-flex items-center gap-1 text-brand hover:underline"
                          >
                            {item.homepage}
                            <ExternalLink className="size-3" aria-hidden />
                          </a>
                        ) : (
                          <span>—</span>
                        )}
                        <span>
                          {t('common.license.colLicense')}: {item.license}
                        </span>
                      </div>
                    ) : null}
                  </td>
                  <td className="px-3 py-2.5 align-top tabular-nums text-ink-3">{item.version || '—'}</td>
                  <td className="px-3 py-2.5 align-top">
                    <Badge variant={licenseBadgeVariant(item.license)} className="h-5 px-1.5 text-[10px]">
                      {item.license}
                    </Badge>
                  </td>
                  <td className="max-w-[220px] truncate px-3 py-2.5 align-top text-ink-3" title={item.author}>
                    {item.author || '—'}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

