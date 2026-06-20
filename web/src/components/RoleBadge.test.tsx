// RoleBadge 测试：锁定 bungee→「BC 代理」紫、bukkit→「子服」蓝，未知角色原样显示。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import RoleBadge from './RoleBadge'

describe('RoleBadge', () => {
  it('bungee 显示「BC 代理」', () => {
    render(<RoleBadge role="bungee" />)
    expect(screen.getByText('BC 代理')).toBeInTheDocument()
  })

  it('bukkit 显示「子服」', () => {
    render(<RoleBadge role="bukkit" />)
    expect(screen.getByText('子服')).toBeInTheDocument()
  })

  it('未知角色原样显示', () => {
    render(<RoleBadge role="proxy" />)
    expect(screen.getByText('proxy')).toBeInTheDocument()
  })
})
