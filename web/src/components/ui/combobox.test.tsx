// Combobox 组件单测（FR-51）：锁定「下拉 + 可编辑」两种模式的核心行为——
// ① editable：键入即上报、可提交候选列表外的新值；② strict：键入仅过滤、列表外值不上报；
// ③ 键入子串过滤候选；④ 点选候选上报该值；⑤ 无匹配候选给空态提示。
import { describe, it, expect, vi } from 'vitest'
import { useState } from 'react'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Combobox, type ComboboxOption } from './combobox'

// 受控包装：把内部 value 暴露到 data 属性，便于断言上报值。
function Harness({
  allowCustom,
  options,
  initial = '',
}: {
  allowCustom: boolean
  options: Array<string | ComboboxOption>
  initial?: string
}) {
  const [value, setValue] = useState(initial)
  return (
    <div>
      <span data-testid="value">{value}</span>
      <Combobox
        aria-label="维度"
        value={value}
        onChange={setValue}
        options={options}
        allowCustom={allowCustom}
        placeholder="请选择"
      />
    </div>
  )
}

const OPTS = ['prod', 'test', 'staging']

describe('Combobox（FR-51）', () => {
  it('点击展开后列出全部候选', async () => {
    render(<Harness allowCustom options={OPTS} />)
    await userEvent.click(screen.getByLabelText('维度'))
    const opts = screen.getAllByRole('option').map((o) => o.textContent)
    expect(opts).toEqual(['prod', 'test', 'staging'])
  })

  it('键入子串过滤候选（大小写无关）', async () => {
    render(<Harness allowCustom options={OPTS} />)
    const input = screen.getByLabelText('维度')
    await userEvent.click(input)
    await userEvent.type(input, 'ST')
    const opts = screen.getAllByRole('option').map((o) => o.textContent)
    expect(opts).toEqual(['test', 'staging'])
  })

  it('点选候选项后上报该值', async () => {
    render(<Harness allowCustom options={OPTS} />)
    await userEvent.click(screen.getByLabelText('维度'))
    await userEvent.click(screen.getByRole('option', { name: 'test' }))
    expect(screen.getByTestId('value')).toHaveTextContent('test')
  })

  it('editable 模式：键入列表外新值即上报（放行自定义值）', async () => {
    render(<Harness allowCustom options={OPTS} />)
    const input = screen.getByLabelText('维度')
    await userEvent.type(input, 'brand-new')
    expect(screen.getByTestId('value')).toHaveTextContent('brand-new')
  })

  it('strict 模式：键入列表外值不上报（仅用于过滤）', async () => {
    render(<Harness allowCustom={false} options={OPTS} />)
    const input = screen.getByLabelText('维度')
    await userEvent.click(input)
    await userEvent.type(input, 'zzz')
    // 列表外键入不应改变受控值
    expect(screen.getByTestId('value')).toHaveTextContent('')
  })

  it('strict 模式：仍可点选候选项上报', async () => {
    render(<Harness allowCustom={false} options={OPTS} />)
    await userEvent.click(screen.getByLabelText('维度'))
    await userEvent.click(screen.getByRole('option', { name: 'prod' }))
    expect(screen.getByTestId('value')).toHaveTextContent('prod')
  })

  it('无匹配候选时给中文空态提示', async () => {
    render(<Harness allowCustom={false} options={OPTS} />)
    const input = screen.getByLabelText('维度')
    await userEvent.click(input)
    await userEvent.type(input, 'zzz')
    expect(screen.getByText('无匹配候选')).toBeInTheDocument()
    expect(screen.queryAllByRole('option')).toHaveLength(0)
  })

  it('onChange 透传所选值给上层', async () => {
    const onChange = vi.fn()
    function Plain() {
      return (
        <Combobox aria-label="维度" value="" onChange={onChange} options={OPTS} allowCustom />
      )
    }
    render(<Plain />)
    await userEvent.click(screen.getByLabelText('维度'))
    await userEvent.click(within(screen.getByRole('listbox')).getByText('staging'))
    expect(onChange).toHaveBeenCalledWith('staging')
  })

  // ===== 值/显示分离的 {value,label} 候选（FR-70）=====
  // 环境维度需「编码 code」作真实值、显示「编码 · 名称」。下拉只显示 label，
  // 选中 / 过滤 / 上报仍以 value（code）为准，API 过滤值不受 name 污染。
  const LV_OPTS = [
    { value: 'prod', label: 'prod · 生产' },
    { value: 'test', label: 'test · 测试' },
  ]

  it('{value,label} 候选：候选列表显示 label 而非 value', async () => {
    render(<Harness allowCustom options={LV_OPTS} />)
    await userEvent.click(screen.getByLabelText('维度'))
    const opts = screen.getAllByRole('option').map((o) => o.textContent)
    expect(opts).toEqual(['prod · 生产', 'test · 测试'])
  })

  it('{value,label} 候选：点选后上报 value（code），不含 name', async () => {
    render(<Harness allowCustom options={LV_OPTS} />)
    await userEvent.click(screen.getByLabelText('维度'))
    await userEvent.click(screen.getByRole('option', { name: 'prod · 生产' }))
    expect(screen.getByTestId('value')).toHaveTextContent('prod')
  })

  it('{value,label} 候选：按 name 子串也能过滤命中', async () => {
    render(<Harness allowCustom={false} options={LV_OPTS} />)
    const input = screen.getByLabelText('维度')
    await userEvent.click(input)
    await userEvent.type(input, '生产')
    const opts = screen.getAllByRole('option').map((o) => o.textContent)
    expect(opts).toEqual(['prod · 生产'])
  })

  it('{value,label} 候选：严格模式关闭后输入框回显已选 label', async () => {
    render(<Harness allowCustom={false} options={LV_OPTS} initial="test" />)
    // 初值为 code=test，控件应回显其 label
    expect(screen.getByLabelText('维度')).toHaveValue('test · 测试')
  })

  // ===== 一键清空（FR-63 底座能力）=====
  it('clearable：值非空时显示清空按钮，点击回传空值', async () => {
    const onClear = vi.fn()
    function ClearHarness() {
      const [v, setV] = useState('prod')
      return (
        <div>
          <span data-testid="value">{v}</span>
          <Combobox
            aria-label="维度"
            value={v}
            onChange={(next) => {
              setV(next)
              if (next === '') onClear()
            }}
            options={OPTS}
            allowCustom
            clearable
            clearLabel="清空"
          />
        </div>
      )
    }
    render(<ClearHarness />)
    await userEvent.click(screen.getByLabelText('清空'))
    expect(screen.getByTestId('value')).toHaveTextContent('')
    expect(onClear).toHaveBeenCalledTimes(1)
  })

  it('clearable：值为空时不渲染清空按钮', () => {
    render(
      <Combobox
        aria-label="维度"
        value=""
        onChange={() => {}}
        options={OPTS}
        allowCustom
        clearable
        clearLabel="清空"
      />,
    )
    expect(screen.queryByLabelText('清空')).not.toBeInTheDocument()
  })
})
