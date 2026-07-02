// Beacon 管理台前端 ESLint 扁平配置（ESLint 9+ flat config）。
// 职责分工：ESLint 管代码质量，格式交给 Prettier（末尾接 eslint-config-prettier 关掉冲突的格式规则）。
import js from '@eslint/js'
import globals from 'globals'
import tseslint from 'typescript-eslint'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import prettier from 'eslint-config-prettier'

export default tseslint.config(
  // 忽略产物 / 覆盖率 / 依赖目录 / vendored Monaco 资产（均非本仓源码，不参与检查）
  { ignores: ['dist', 'coverage', 'node_modules', 'public/monaco'] },
  // 基础推荐集：JS 通用 + typescript-eslint 非类型检查推荐（避免开 type-checked 拖慢 / 过严）
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    // 仅检查 src 下的 TS/TSX
    files: ['src/**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      // 浏览器全局：window / document / fetch 等
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      // React Hooks 推荐规则（rules-of-hooks 报错 + exhaustive-deps 告警）
      ...reactHooks.configs.recommended.rules,
      // Fast Refresh DX 提示规则：本仓刻意把「与组件同测的纯函数 / 自定义 hook」及 shadcn 的 cva
      // variants（cva() 返回函数，非常量、allowConstantExport 不覆盖）与组件同文件导出，
      // 拆文件属对正常代码的越界重构。该规则仅影响热更新体验、不涉正确性，故集中关闭（见 static-analysis.md）。
      'react-refresh/only-export-components': 'off',
      // 以下划线前缀的形参 / 变量表示「有意未用」（如解构剔除字段），不报未用
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
    },
  },
  {
    // E2E（Playwright）用例、共享步骤与构建脚本：运行在 Node 环境，
    // 提供 node 全局（process / console / URL 等），避免 no-undef 误报。
    files: ['e2e/**/*.{ts,mts,mjs}', 'scripts/**/*.mjs', 'playwright.config.ts'],
    languageOptions: {
      globals: globals.node,
    },
  },
  // 末尾接 prettier，关闭所有与 Prettier 冲突的格式类规则（缩进 / 引号 / 分号等格式归 Prettier 管）
  prettier,
)
