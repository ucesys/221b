import { isWebUrl } from '../../api/knowledge'

/**
 * A source's title, linked when its address opens in a browser. An object
 * of a bucket of documents (KN-25) is named by its s3://bucket/key beside
 * the title instead: no presigned address, nothing to click. A record of
 * the console (a fix, KN-33) links to it in the console.
 */
export function SourceLink({ title, url }: { title: string; url?: string }) {
  if (url?.startsWith('#/')) {
    return (
      <a className="rk-link" href={url}>
        {title}
      </a>
    )
  }
  if (isWebUrl(url)) {
    return (
      <a className="rk-link" href={url} target="_blank" rel="noreferrer">
        {title}
      </a>
    )
  }
  if (url) {
    return (
      <>
        {title}{' '}
        <span className="rk-mono rk-fhint" style={{ overflowWrap: 'anywhere' }} data-testid="source-location">
          {url}
        </span>
      </>
    )
  }
  return <>{title}</>
}
