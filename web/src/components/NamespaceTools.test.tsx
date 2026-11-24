// New namespace… in the namespace's wrench on a cluster that takes no
// change (RB-36): hidden on a read-only connection, off with the reason
// where strict refuses the cluster, on otherwise.
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NamespaceTools } from './NamespaceTools'

const strict = 'Changes are refused on this cluster because rbac.enforce is strict.'

function open(props: { createOff?: string; createHidden?: boolean }, onPick = vi.fn()) {
  render(<NamespaceTools namespace="web" canCreate {...props} onPick={onPick} />)
  fireEvent.click(screen.getByTestId('tools-namespace-web'))
  return onPick
}

describe('NamespaceTools', () => {
  afterEach(cleanup)

  it('hides New namespace… on a read-only connection', () => {
    open({ createHidden: true })
    expect(screen.queryByTestId('namespace-tool-new')).toBeNull()
    expect(screen.getByTestId('namespace-tool-delete')).toBeTruthy()
  })

  it('turns New namespace… off with the reason where strict refuses the cluster', () => {
    const onPick = open({ createOff: strict })
    const item = screen.getByTestId('namespace-tool-new')
    expect(item.getAttribute('aria-disabled')).toBe('true')
    expect(item.textContent).toContain(strict)
    fireEvent.click(item)
    expect(onPick).not.toHaveBeenCalled()
  })

  it('offers New namespace… on a connection that takes changes', () => {
    const onPick = open({})
    const item = screen.getByTestId('namespace-tool-new')
    expect(item.getAttribute('aria-disabled')).toBeNull()
    fireEvent.click(item)
    expect(onPick).toHaveBeenCalledWith('new')
  })
})
