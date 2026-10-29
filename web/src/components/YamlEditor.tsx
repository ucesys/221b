import { useEffect, useRef } from 'react'
import { indentWithTab } from '@codemirror/commands'
import { yaml } from '@codemirror/lang-yaml'
import { HighlightStyle, indentUnit, syntaxHighlighting } from '@codemirror/language'
import { linter, type Diagnostic } from '@codemirror/lint'
import { Compartment, EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, placeholder as cmPlaceholder } from '@codemirror/view'
import { tags } from '@lezer/highlight'
import { minimalSetup } from 'codemirror'
import { parseDocument } from 'yaml'

/** The YAML the person wrote, with its problems; none is clean. */
export type YamlChange = { text: string; errors: string[] }

interface Props {
  value: string
  onChange: (c: YamlChange) => void
  onBlur?: () => void
  readOnly?: boolean
  label: string
  testId: string
  minHeight?: number | string
  placeholder?: string
}

// The theme's colours, so the editor follows light and dark (M-06).
const theme = EditorView.theme({
  '&': { background: 'var(--surface)', color: 'var(--fg)', border: '1px solid var(--line)', borderRadius: '8px', fontSize: '12.5px', fontFamily: 'var(--mono)' },
  '&.cm-focused': { outline: '2px solid var(--accent)', outlineOffset: '-1px' },
  '.cm-scroller': { fontFamily: 'var(--mono)', lineHeight: '1.55', overflow: 'auto', borderRadius: '8px' },
  '.cm-content': { padding: '8px 0', caretColor: 'var(--fg)' },
  '.cm-line': { padding: '0 10px' },
  '.cm-gutters': { background: 'var(--sunken)', color: 'var(--fg3)', border: 'none', borderRight: '1px solid var(--line)', borderRadius: '8px 0 0 8px' },
  '.cm-activeLineGutter': { background: 'transparent', color: 'var(--fg2)' },
  '.cm-placeholder': { color: 'var(--fg3)', fontStyle: 'normal' },
  '.cm-lintRange-error': { backgroundImage: 'none', textDecoration: 'underline wavy var(--err)', textUnderlineOffset: '3px' },
  '.cm-tooltip': { background: 'var(--surface)', color: 'var(--fg)', border: '1px solid var(--line)', borderRadius: '6px', fontFamily: 'var(--sans)' },
  '.cm-diagnostic-error': { borderLeftColor: 'var(--err)' },
})
const highlight = HighlightStyle.define([
  { tag: tags.propertyName, color: 'var(--info)' },
  { tag: [tags.string, tags.special(tags.string)], color: 'var(--ok)' },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: 'var(--warn)' },
  { tag: tags.comment, color: 'var(--fg3)', fontStyle: 'italic' },
  { tag: [tags.keyword, tags.meta, tags.punctuation], color: 'var(--fg2)' },
])

/** What is wrong with the YAML, as the yaml parser sees it. */
export function yamlErrors(text: string): { from: number; to: number; message: string }[] {
  const doc = parseDocument(text, { prettyErrors: false })
  return doc.errors.map((e) => ({ from: e.pos[0], to: Math.max(e.pos[1], e.pos[0] + 1), message: e.message.split('\n')[0]! }))
}

/**
 * A YAML editor (M-06): line numbers, highlighting, Tab indents two
 * spaces, and the parser's errors underlined and named. The text node
 * carries the test id and the label, as a textarea would.
 */
export function YamlEditor({ value, onChange, onBlur, readOnly = false, label, testId, minHeight = 200, placeholder }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const change = useRef(onChange)
  const blur = useRef(onBlur)
  const ro = useRef(new Compartment())
  useEffect(() => {
    change.current = onChange
    blur.current = onBlur
  }, [onChange, onBlur])
  useEffect(() => {
    if (!host.current) return
    const min = typeof minHeight === 'number' ? `${minHeight}px` : minHeight
    const v = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          minimalSetup,
          lineNumbers(),
          yaml(),
          syntaxHighlighting(highlight),
          theme,
          EditorView.theme({ '.cm-content, .cm-gutter': { minHeight: min } }),
          indentUnit.of('  '),
          EditorState.tabSize.of(2),
          keymap.of([indentWithTab]),
          linter((e) => yamlErrors(e.state.doc.toString()).map((d): Diagnostic => ({ ...d, severity: 'error' })), { delay: 300 }),
          EditorView.contentAttributes.of({ 'data-testid': testId, 'aria-label': label, 'aria-multiline': 'true' }),
          placeholder ? cmPlaceholder(placeholder) : [],
          ro.current.of([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) {
              const text = u.state.doc.toString()
              change.current({ text, errors: yamlErrors(text).map((d) => d.message) })
            }
            if (u.focusChanged && !u.view.hasFocus) blur.current?.()
          }),
        ],
      }),
    })
    view.current = v
    return () => {
      v.destroy()
      view.current = null
    }
    // The editor is made once; value and readOnly changes are dispatched below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useEffect(() => {
    const v = view.current
    if (!v) return
    const now = v.state.doc.toString()
    if (now !== value && !v.hasFocus) v.dispatch({ changes: { from: 0, to: now.length, insert: value } })
  }, [value])
  useEffect(() => {
    view.current?.dispatch({ effects: ro.current.reconfigure([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]) })
  }, [readOnly])
  return <div ref={host} className="rk-yaml" data-readonly={readOnly || undefined} />
}
