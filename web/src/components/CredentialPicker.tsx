import { useEffect, useRef, useState } from 'react'
import { fetchCredentialChoices, KIND_LABEL, type CredentialChoice, type CredentialUse } from '../api/credentials'
import { adminHref } from '../lib/router'

/** The select's value while nothing is chosen. */
const CHOOSE = '__choose'

const NONE: CredentialChoice[] = []

interface Props {
  /** What the credential is for: the picker offers only what serves it. */
  use: CredentialUse
  /** The address it goes to: only credentials whose endpoint covers it are offered. */
  address: string
  /** The credential chosen, by id; "" is none; null is not chosen yet. */
  value: string | null
  /** A choice; null when the address changed and several cover the new one. */
  onChange: (id: string | null) => void
  /** The label over the select. */
  label?: string
  /** The name of "none": "None (public)" by default. */
  noneLabel?: string
  /** More choices after the credentials, such as a bucket's pod identity. */
  extra?: { value: string; label: string }[]
  disabled?: boolean
  testId?: string
  /** A saved credential's name, shown when it no longer covers the address. */
  savedName?: string
}

/**
 * A credential of Settings › Credentials, chosen by name (0.12.1, CR-10):
 * those that serve the use and cover the address. The only one is
 * preselected while nothing is chosen; among several the person must
 * choose: it never guesses. "None (public)" is always there. The page
 * keeps the id only, never a secret.
 */
export function CredentialPicker({ use, address, value, onChange, label = 'Credential', noneLabel = 'None (public)', extra = [], disabled, testId = 'credential-picker', savedName }: Props) {
  const [choices, setChoices] = useState<{ key: string; list: CredentialChoice[] } | null>(null)
  const [error, setError] = useState('')
  const key = `${use}\n${address.trim()}`
  useEffect(() => {
    const addr = address.trim()
    if (!addr) return
    let stop = false
    const t = setTimeout(() => {
      fetchCredentialChoices(use, addr).then(
        (list) => {
          if (stop) return
          setChoices({ key, list })
          setError('')
        },
        (e: unknown) => !stop && (setChoices({ key, list: [] }), setError(e instanceof Error ? e.message : String(e))),
      )
    }, 250)
    return () => {
      stop = true
      clearTimeout(t)
    }
  }, [use, address, key])
  // No address, no credential covers it.
  const list = !address.trim() ? NONE : choices?.key === key ? choices.list : null
  // The only one is taken while nothing is chosen; none, and none is. What
  // the picker chose itself is chosen again when the address changes; what
  // the person chose stays.
  const auto = useRef<{ key: string; value: string } | null>(null)
  useEffect(() => {
    if (!list) return
    const mine = auto.current !== null && auto.current.value === value && auto.current.key !== key
    if (value !== null && !mine) return
    const next = list.length === 1 ? list[0]!.id : list.length === 0 ? '' : null
    auto.current = next === null ? null : { key, value: next }
    if (next !== value) onChange(next)
  }, [list, value, onChange, key])
  const known = list?.some((c) => c.id === value) || value === '' || extra.some((x) => x.value === value)
  const several = !!list && list.length > 1 && value === null
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12, color: 'var(--fg2)', minWidth: 0 }} data-testid={testId}>
      {label}
      <span className="rk-field">
        <select
          value={value ?? CHOOSE}
          disabled={disabled || !list}
          onChange={(e) => {
            if (e.target.value === CHOOSE) return
            auto.current = null
            onChange(e.target.value)
          }}
          aria-invalid={several || undefined}
          data-testid={`${testId}-select`}
        >
          {value === null && (
            <option value={CHOOSE} disabled>
              {several ? `Choose one of ${list!.length}…` : 'Choose…'}
            </option>
          )}
          <option value="">{noneLabel}</option>
          {(list ?? []).map((c) => (
            <option key={c.id} value={c.id}>
              {c.name} · {KIND_LABEL[c.kind]} · {c.endpoint}
            </option>
          ))}
          {extra.map((x) => (
            <option key={x.value} value={x.value}>
              {x.label}
            </option>
          ))}
          {value && list && !known && <option value={value}>{savedName ?? 'The saved credential'} (does not cover this address)</option>}
        </select>
      </span>
      <span className="rk-fhint" data-testid={`${testId}-hint`}>
        {!list
          ? 'Looking for credentials…'
          : several
            ? `${list.length} credentials cover this address: choose one.`
            : value && !known
              ? 'The saved credential does not cover this address: choose another, or none.'
              : list.length === 0
                ? 'No credential covers this address.'
                : `${list.length} ${list.length === 1 ? 'credential covers' : 'credentials cover'} this address.`}{' '}
        <a href={adminHref('settings', ['credentials'])} className="rk-link">
          Settings › Credentials
        </a>
        {error && <span style={{ color: 'var(--err)' }}> {error}</span>}
      </span>
    </label>
  )
}
