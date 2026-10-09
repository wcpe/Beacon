import type {SidebarsConfig} from '@docusaurus/plugin-content-docs'

/**
 * 使用者文档侧栏。
 *
 * 显式枚举而非自动生成：文档站只面向使用者，目录结构应当由「读者上手顺序」决定，
 * 而不是由仓库的物理目录决定。新增使用者文档时需在此登记（有意为之——避免内部文档
 * 因目录扫描而意外进站）。
 */
const sidebars: SidebarsConfig = {
  userDocs: [
    {
      type: 'category',
      label: '开始使用',
      collapsed: false,
      items: ['wiki/README', 'wiki/quick-start', 'wiki/build-a-cluster'],
    },
    {
      type: 'category',
      label: '功能指南',
      collapsed: false,
      items: [
        'wiki/configuration-and-delivery',
        'wiki/global-lobby-operations',
        'wiki/approval-lifecycle-and-mcp',
        'wiki/observability-and-operations',
        'wiki/business-plugin-sdk',
      ],
    },
    {
      type: 'category',
      label: '配置参考',
      items: ['wiki/bukkit-agent-full-config', 'wiki/bc-agent-full-config'],
    },
    {
      type: 'category',
      label: '运维与排障',
      items: [
        'OPERATIONS',
        'wiki/security-and-maintenance',
        'wiki/troubleshooting',
        'SDK',
      ],
    },
  ],
}

export default sidebars
