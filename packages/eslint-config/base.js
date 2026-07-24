import js from '@eslint/js'
import prettier from 'eslint-config-prettier'
import tseslint from 'typescript-eslint'

const ignoredPaths = [
  '**/dist/**',
  '**/coverage/**',
  '**/node_modules/**',
  '**/playwright-report/**',
  '**/test-results/**',
]

export default tseslint.config(
  {
    ignores: ignoredPaths,
  },
  js.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  ...tseslint.configs.stylisticTypeChecked,
  prettier,
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      parserOptions: {
        projectService: true,
      },
    },
    linterOptions: {
      reportUnusedDisableDirectives: 'error',
    },
    rules: {
      'no-console': 'error',
      'no-debugger': 'error',
    },
  },
)
