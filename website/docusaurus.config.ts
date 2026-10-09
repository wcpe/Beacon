import type {Config} from '@docusaurus/types'
import type {Options as DocsOptions} from '@docusaurus/plugin-content-docs'
import {themes as prismThemes} from 'prism-react-renderer'
import remarkInternalDocLinks from './src/remark/internal-doc-links.ts'

/**
 * Beacon 文档站配置。
 *
 * 设计要点（与仓库纪律一致）：
 * - **内容零搬迁**：docs 插件直接读取仓库既有 `docs/`，站点不复制任何 Markdown；
 *   因此「改文档」与「站点更新」天然同步，不存在双写与漂移。
 * - **白名单收录**：`include` 只收面向使用者的 14 篇（wiki 12 + OPERATIONS + SDK）。
 *   内部文档（adr / specs / PRD / ROADMAP / UX / UI-WIKI / API 等）物理上进不来；
 *   新增顶层文档默认不进站（失败方向安全）。
 * - 站点部署走独立的 `.github/workflows/docs.yml`，不阻塞代码 CI。
 */

/** 站内收录的使用者文档（白名单；与 README「文档」一节口径一致）。 */
const USER_DOC_INCLUDE = ['wiki/**/*.md', 'OPERATIONS.md', 'SDK.md']

/** 明确排除的内部文档（双保险：即便白名单放宽也不会进站）。 */
const INTERNAL_DOC_EXCLUDE = [
  'adr/**',
  'specs/**',
  'PRD.md',
  'ROADMAP.md',
  'ARCHITECTURE.md',
  'API.md',
  'API-v1-legacy.md',
  'UX.md',
  'UI-WIKI.md',
  'CONTRIBUTING.md',
]

const config: Config = {
  title: 'Beacon',
  tagline: '面向 Minecraft 多群组服务器的集群调度中间件控制面',
  favicon: 'img/dashboard.png',

  url: 'https://wcpe.github.io',
  baseUrl: '/Beacon/',
  organizationName: 'wcpe',
  projectName: 'Beacon',
  trailingSlash: false,

  // 文档均为中文；站点 UI 与 <html lang> 同步为 zh-Hans。
  i18n: {defaultLocale: 'zh-Hans', locales: ['zh-Hans']},

  onBrokenLinks: 'throw',

  markdown: {
    // 使用者文档全部是 CommonMark（无 JSX）；按扩展名识别，避免 MDX 误解析中文正文里的尖括号。
    format: 'detect',
    hooks: {
      // 相对链接解析失败即构建失败：使用者文档不得留下指向站外文件的悬空链接。
      onBrokenMarkdownLinks: 'throw',
    },
  },

  presets: [
    [
      'classic',
      {
        // 本站只做文档，不需要博客。
        blog: false,
        docs: {
          path: '../docs',
          include: USER_DOC_INCLUDE,
          exclude: INTERNAL_DOC_EXCLUDE,
          routeBasePath: 'docs',
          sidebarPath: './sidebars.ts',
          // 精确指回仓库 docs/ 下的真实文件（站点是「读取」而非「副本」）。
          editUrl: ({docPath}) =>
            `https://github.com/wcpe/Beacon/edit/master/docs/${docPath}`,
          // 把指向内部目录的相对链接重写为仓库绝对链接（使用者文档已零内部引用；
          // 此插件是护栏：即便将来误加，也不会在站内断链或暴露内部路径）。
          remarkPlugins: [remarkInternalDocLinks],
        },
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies DocsOptions & {blog: false; theme: {customCss: string}},
    ],
  ],

  themeConfig: {
    navbar: {
      title: 'Beacon',
      logo: {alt: 'Beacon', src: 'img/dashboard.png'},
      items: [
        {type: 'docSidebar', sidebarId: 'userDocs', position: 'left', label: '文档'},
        {
          href: 'https://github.com/wcpe/Beacon',
          label: 'GitHub',
          position: 'right',
        },
        {
          href: 'https://github.com/wcpe/Beacon/releases',
          label: '下载',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: '文档',
          items: [
            {label: '快速开始', to: '/docs/wiki/quick-start'},
            {label: '搭建集群', to: '/docs/wiki/build-a-cluster'},
            {label: '配置与交付', to: '/docs/wiki/configuration-and-delivery'},
          ],
        },
        {
          title: '参考',
          items: [
            {label: '部署与运维', to: '/docs/OPERATIONS'},
            {label: '业务插件 SDK', to: '/docs/SDK'},
            {label: '排障', to: '/docs/wiki/troubleshooting'},
          ],
        },
        {
          title: '项目',
          items: [
            {label: 'GitHub', href: 'https://github.com/wcpe/Beacon'},
            {
              label: 'Releases',
              href: 'https://github.com/wcpe/Beacon/releases',
            },
            {
              label: '问题反馈',
              href: 'https://github.com/wcpe/Beacon/issues',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} wcpe · MIT License`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'kotlin', 'java', 'yaml', 'json', 'diff'],
    },
    docs: {
      sidebar: {hideable: true},
    },
  },
}

export default config
