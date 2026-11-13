import type { ImageCheck, NewerImage } from '../../api/apps'

// Newer images (HA-21 to HA-23): what the release's panel and the Helm
// tab's table say when a registry publishes a newer image than a release
// runs. A registry that could not be asked says nothing loud: an
// air-gapped cluster sees no notice and no error.

/** "ghcr.io/acme/api:1.2.4 (running 1.2.3)", or, for a tag that moved, "nginx:latest has a newer image (sha256:…)". */
export function newerImageText(n: NewerImage): string {
  const repo = (n.registry ? n.registry + '/' : '') + n.repository
  if (n.digest && n.newer === n.tag) return `${repo}:${n.newer} points at a newer image (${n.digest.slice(0, 19)}…)`
  return `${repo}:${n.newer} (running ${n.tag ?? 'latest'})`
}

/** The notice at the top of a release's panel: the first newer image, and how many more. */
export function NewerImageNotice({ images, onImages }: { images?: NewerImage[]; onImages?: () => void }) {
  const [first, ...rest] = images ?? []
  if (!first) return null
  return (
    <div className="rk-callout" data-tone="info" data-testid="app-newer-image">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
        <strong style={{ fontSize: 12.5, fontWeight: 600, overflowWrap: 'anywhere' }}>A newer image is available: {newerImageText(first)}</strong>
        <span style={{ fontSize: 12.5, color: 'var(--fg2)' }}>
          {rest.length > 0 && <span style={{ overflowWrap: 'anywhere' }}>Also {rest.map(newerImageText).join('; ')}. </span>}
          Upgrading the chart, or its values, is what changes the image.{' '}
          {onImages && (
            <button className="rk-link" onClick={onImages} data-testid="app-newer-image-open">
              See the images
            </button>
          )}
        </span>
      </div>
    </div>
  )
}

/** The marker on a release's row in the Helm tab's table. */
export function NewerImageMark({ images }: { images?: NewerImage[] }) {
  if (!images?.length) return null
  return (
    <span className="rk-tag" data-tone="info" style={{ fontSize: 10.5, padding: '1px 6px', marginLeft: 6 }} title={'A newer image is available:\n' + images.map(newerImageText).join('\n')} data-testid="app-newer-image-mark">
      newer image
    </span>
  )
}

/** What the Images tab says next to a tag: the newer one, or quietly that it could not be checked. */
export function ImageCheckNote({ check }: { check?: ImageCheck }) {
  if (!check) return null
  switch (check.state) {
    case 'newer':
      return (
        <span className="rk-mono" style={{ display: 'block', fontSize: 11.5, color: 'var(--accent)' }} title={check.digest ? `The tag now points at ${check.digest}` : undefined} data-testid="app-image-newer">
          {check.digest ? 'newer image under this tag' : `${check.newer} available`}
        </span>
      )
    case 'unknown':
      return (
        <span style={{ display: 'block', fontSize: 11.5, color: 'var(--fg3)' }} title={check.reason} data-testid="app-image-unchecked">
          could not check
        </span>
      )
    case 'pending':
      return (
        <span style={{ display: 'block', fontSize: 11.5, color: 'var(--fg3)' }} data-testid="app-image-pending">
          checking…
        </span>
      )
  }
  return null
}
