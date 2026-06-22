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
 */

import { useRef, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import Editor, { DiffEditor, type OnMount } from '@monaco-editor/react'
import type { editor } from 'monaco-editor'

// ---- 类型 ----

interface CodeEditorProps {
  value?: string
  original?: string
  modified?: string
  language?: string
  onChange?: (value: string) => void
  onMount?: () => void
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
}: CodeEditorProps) {
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null)

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

  if (original || modified) {
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
    <Editor
      value={value}
      language={monacoLang}
      theme="vs"
      onChange={(v) => onChange?.(v ?? '')}
      onMount={handleEditorMount}
      options={editOptions as any}
      loading={<EditorLoading />}
    />
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
