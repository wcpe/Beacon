// DataTable 单测：表头、行渲染、空态、行点击 + 紧凑密度（FR-92）。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import DataTable, { type DataTableColumn } from './DataTable'
import { setDensity } from '@/state/preferences'

// 每个用例前复位密度为舒适，避免跨用例污染（store 为模块单例）
beforeEach(() => {
  localStorage.clear()
  setDensity('comfortable')
})

interface Row {
  id: number
  name: string
}

const columns: DataTableColumn<Row>[] = [
  { header: '编号', cell: (r) => r.id },
  { header: '名称', cell: (r) => r.name },
]

describe('DataTable', () => {
  it('渲染表头与行数据', () => {
    const rows: Row[] = [
      { id: 1, name: '甲' },
      { id: 2, name: '乙' },
    ]
    render(<DataTable columns={columns} rows={rows} rowKey={(r) => String(r.id)} />)
    expect(screen.getByText('编号')).toBeInTheDocument()
    expect(screen.getByText('名称')).toBeInTheDocument()
    expect(screen.getByText('甲')).toBeInTheDocument()
    expect(screen.getByText('乙')).toBeInTheDocument()
  })

  it('空数据显示空态文案且跨所有列', () => {
    render(
      <DataTable columns={columns} rows={[]} rowKey={(r) => String(r.id)} emptyText="暂无环境" />,
    )
    const cell = screen.getByText('暂无环境')
    expect(cell).toBeInTheDocument()
    expect(cell).toHaveAttribute('colspan', String(columns.length))
  })

  it('rows 为 undefined 时按空态处理', () => {
    render(<DataTable columns={columns} rows={undefined} rowKey={(r) => String(r.id)} />)
    expect(screen.getByText('暂无数据')).toBeInTheDocument()
  })

  it('点击行触发回调', async () => {
    const onRowClick = vi.fn()
    const rows: Row[] = [{ id: 1, name: '甲' }]
    render(
      <DataTable
        columns={columns}
        rows={rows}
        rowKey={(r) => String(r.id)}
        onRowClick={onRowClick}
      />,
    )
    await userEvent.click(screen.getByText('甲'))
    expect(onRowClick).toHaveBeenCalledWith(rows[0])
  })

  it('紧凑密度时表头与单元格渲染出紧凑样式类（FR-92）', () => {
    setDensity('compact')
    const rows: Row[] = [{ id: 1, name: '甲' }]
    render(<DataTable columns={columns} rows={rows} rowKey={(r) => String(r.id)} />)
    // 表头收紧高度 h-8（替代默认 h-10），单元格收紧上下内边距 py-1
    expect(screen.getByText('编号').classList.contains('h-8')).toBe(true)
    expect(screen.getByText('甲').classList.contains('py-1')).toBe(true)
  })

  it('舒适密度时不加紧凑样式类（FR-92）', () => {
    setDensity('comfortable')
    const rows: Row[] = [{ id: 1, name: '甲' }]
    render(<DataTable columns={columns} rows={rows} rowKey={(r) => String(r.id)} />)
    expect(screen.getByText('编号').classList.contains('h-8')).toBe(false)
    expect(screen.getByText('甲').classList.contains('py-1')).toBe(false)
  })
})
