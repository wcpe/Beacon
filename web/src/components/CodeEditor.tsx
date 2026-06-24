/**
 * Monaco 代码编辑器组件
 *
 * 支持两种模式：
 * - edit: Monaco Editor（编辑配置内容）
 * - diff: Monaco DiffEditor（对比两个版本差异）
 *
 * 特性：
 * - yaml/json/properties 语法高亮
 * - Ctrl+S 保存快捷键
 * - 自动缩进、括号匹配、代码折叠
 * - 行号、自动换行、查找替换
 * - 亮色主题
 * - 客户端格式校验（FR-75）：编辑模式下解析失败时编辑器旁显示行内错误条，
 *   并经 onValidate 上抛错误供上层禁用发布
 */

import { useRef, useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import Editor, { DiffEditor, type OnMount } from '@monaco-editor/react'
import type { editor } from 'monaco-editor'
import { lintContent, type LintError } from '@/lib/configLint'

// ---- 类型 ----

interface CodeEditorProps {
  value?: string
  original?: string
  modified?: string
  language?: string
  onChange?: (value: string) => void
  onMount?: () => void
  // 客户端格式校验结果回调（FR-75）：合法上抛 null，非法上抛首个错误。仅编辑模式触发。
  onValidate?: (error: LintError | null) => void
}

// ---- 语言映射 ----

function mapLanguage(format: string): string {
  switch (format) {
    case 'yaml': return 'yaml'
    case 'json': return 'json'
    default: return 'plaintext'
  }
}

// ---- 主组件 ----

export default function CodeEditor({
  value = '',
  original = '',
  modified = '',
  language = 'yaml',
  onChange,
  onMount,
  onValidate,
}: CodeEditorProps) {
  const { t } = useTranslation()
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null)

  // 是否为 diff 模式（只读对比，不做格式校验）
  const isDiff = !!(original || modified)

  // 去抖后的内容：编辑器内容即时显示，校验延迟 250ms 触发，避免大 YAML 每击键同步全量解析阻塞主线程。
  const [debouncedValue, setDebouncedValue] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setDebouncedValue(value), 250)
    return () => clearTimeout(timer)
  }, [value])

  // 校验中：内容已变但去抖未落定，保守视为「待校验」，此窗口禁止保存（不让非法内容漏过）。
  const pending = !isDiff && value !== debouncedValue

  // 对去抖后的内容做客户端格式校验（useMemo 避免每次 render 重复全量解析）；diff 模式恒视为合法（不校验）。
  const lintError = useMemo(
    () => (isDiff ? null : lintContent(language, debouncedValue)),
    [isDiff, language, debouncedValue],
  )

  // 上抛给上层（供禁用发布）：校验中以哨兵错误占位（保守不放行），落定后回真实结果。
  const validateError: LintError | null = pending
    ? { line: 0, message: t('editor.lintValidating') }
    : lintError

  // 校验结果变化时上抛；按行号 + 信息比较避免重复回调
  useEffect(() => {
    if (isDiff) return
    onValidate?.(validateError)
    // 仅在错误标识变化时回调（line+message 唯一标识一条错误）
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isDiff, validateError?.line, validateError?.message])

  const handleEditorMount: OnMount = useCallback((ed) => {
    editorRef.current = ed
    ed.onKeyDown((e) => {
      if ((e.ctrlKey || e.metaKey) && e.keyCode === 49) {
        e.preventDefault()
        window.dispatchEvent(new KeyboardEvent('keydown', {
          key: 's', code: 'KeyS', keyCode: 49, ctrlKey: true, bubbles: true,
        }))
      }
    })
    onMount?.()
  }, [onMount])

  const handleDiffMount = useCallback(() => { onMount?.() }, [onMount])

  const monacoLang = mapLanguage(language)

  // 编辑模式配置
  const editOptions = {
    fontSize: 13,
    fontFamily: 'var(--font-mono)',
    minimap: { enabled: false },
    scrollBeyondLastLine: false,
    automaticLayout: true,
    tabSize: 2,
    padding: { top: 8 },
    scrollbar: { verticalScrollbarSize: 8, horizontalScrollbarSize: 8, useShadows: false },
    smoothScrolling: true,
    cursorBlinking: 'smooth',
    cursorSmoothCaretAnimation: 'explicit',
    renderLineHighlight: 'all',
    lineNumbers: 'on',
    lineNumbersMinChars: 3,
    glyphMargin: true,
    folding: true,
    foldingStrategy: 'indentation',
    bracketPairColorization: { enabled: true },
    guides: { bracketPairs: true, indentation: true },
    wordWrap: 'on',
    wrappingIndent: 'indent',
    autoIndent: 'full',
    formatOnPaste: true,
    formatOnType: true,
    suggestOnTriggerCharacters: true,
    acceptSuggestionOnCommitCharacter: true,
    snippetSuggestions: 'inline',
    tabCompletion: 'on',
    wordBasedSuggestions: 'off' as const,
    parameterHints: { enabled: true, cycle: true },
    hover: { enabled: true },
    links: true,
    mouseWheelZoom: true,
    find: {
      addExtraSpaceOnTop: true,
      autoFindInSelection: 'multiline',
      seedSearchStringFromSelection: 'always',
    },
  }

  // Diff 模式配置
  const diffOptions = {
    ...editOptions,
    readOnly: false,
    renderSideBySide: true,
    renderOverviewRuler: true,
    overviewRulerBorder: false,
    lineDecorationsWidth: 8,
    glyphMargin: false,
    folding: false,
  }

  if (isDiff) {
    return (
      <DiffEditor
        original={original}
        modified={modified || value}
        language={monacoLang}
        theme="vs"
        options={diffOptions as any}
        onMount={handleDiffMount}
        loading={<EditorLoading />}
      />
    )
  }

  return (
    <div className="flex h-full flex-col">
      <div className="min-h-0 flex-1">
        <Editor
          value={value}
          language={monacoLang}
          theme="vs"
          onChange={(v) => onChange?.(v ?? '')}
          onMount={handleEditorMount}
          options={editOptions as any}
          loading={<EditorLoading />}
        />
      </div>
      {/* 客户端格式校验错误条（FR-75）：解析失败时就近标错，含行号 + 信息 */}
      {lintError && (
        <div
          className="flex-shrink-0 border-t border-destructive/40 bg-destructive/10 px-3 py-1.5 text-xs text-destructive"
          role="alert"
        >
          <span className="font-medium">{t('editor.lintErrorTitle')}</span>
          <span className="ml-2">
            {t('editor.lintErrorLine', { line: lintError.line, message: lintError.message })}
          </span>
        </div>
      )}
    </div>
  )
}

function EditorLoading() {
  const { t } = useTranslation()
  return (
    <div className="flex h-full items-center justify-center bg-background text-sm text-muted-foreground">
      {t('editor.loading')}
    </div>
  )
}
