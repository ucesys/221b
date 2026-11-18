import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { NewerImage } from '../../api/apps'
import { ImageCheckNote, NewerImageMark, NewerImageNotice, newerImageText } from './NewerImages'

const api: NewerImage = { image: 'ghcr.io/acme/api:1.2.3', registry: 'ghcr.io', repository: 'acme/api', tag: '1.2.3', newer: '1.2.4' }
const nginx: NewerImage = { image: 'nginx:1.27.0', repository: 'nginx', tag: '1.27.0', newer: '1.27.3' }

describe('newer images (HA-22)', () => {
  afterEach(cleanup)

  it('says the newer image and the running one at the top of the panel', () => {
    const onImages = vi.fn()
    render(<NewerImageNotice images={[api, nginx]} onImages={onImages} />)
    const notice = screen.getByTestId('app-newer-image')
    expect(notice.textContent).toContain('A newer image is available: ghcr.io/acme/api:1.2.4 (running 1.2.3)')
    expect(notice.textContent).toContain('Also nginx:1.27.3 (running 1.27.0)')
    fireEvent.click(screen.getByTestId('app-newer-image-open'))
    expect(onImages).toHaveBeenCalledTimes(1)
  })

  it('shows nothing when no image is newer', () => {
    const { container } = render(
      <>
        <NewerImageNotice images={[]} />
        <NewerImageNotice />
        <NewerImageMark />
      </>,
    )
    expect(container.textContent).toBe('')
  })

  it('says a tag that moved by its digest', () => {
    const moved: NewerImage = { image: 'nginx:latest@sha256:aaaa', repository: 'nginx', tag: 'latest', newer: 'latest', digest: 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbb' }
    expect(newerImageText(moved)).toBe('nginx:latest points at a newer image (sha256:bbbbbbbbbbbb…)')
  })

  it('marks the row, with the images in its tip', () => {
    render(<NewerImageMark images={[api]} />)
    const mark = screen.getByTestId('app-newer-image-mark')
    expect(mark.textContent).toBe('newer image')
    expect(mark.getAttribute('title')).toContain('ghcr.io/acme/api:1.2.4 (running 1.2.3)')
  })

  it('says quietly on the Images tab when a registry could not be checked', () => {
    const { rerender, container } = render(<ImageCheckNote check={{ state: 'newer', newer: '1.2.4' }} />)
    expect(screen.getByTestId('app-image-newer').textContent).toBe('1.2.4 available')
    rerender(<ImageCheckNote check={{ state: 'unknown', reason: 'the registry could not be reached' }} />)
    const quiet = screen.getByTestId('app-image-unchecked')
    expect(quiet.textContent).toBe('could not check')
    expect(quiet.getAttribute('title')).toBe('the registry could not be reached')
    rerender(<ImageCheckNote check={{ state: 'current' }} />)
    expect(container.textContent).toBe('')
    rerender(<ImageCheckNote check={{ state: 'skipped', reason: 'pinned by digest alone' }} />)
    expect(container.textContent).toBe('')
  })
})
