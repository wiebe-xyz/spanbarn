import { type ReactElement } from 'react'

export type SetupResult = {
  slug: string
  // created is true when this call made the project; only then does the
  // guide carry the ingest key.
  created: boolean
  guide: string
}

// The setup call returns the guide as text. For a new project it holds the
// ingest key, which the server keeps only as a hash, so this is the one
// place it is shown.
export function SetupGuideNotice({ result, onDismiss }: { result: SetupResult; onDismiss: () => void }): ReactElement {
  return (
    <div
      role="status"
      style={{
        border: '1px solid var(--border)',
        borderRadius: 6,
        padding: '0.5rem',
        fontSize: '0.75rem',
        display: 'flex',
        flexDirection: 'column',
        gap: 6,
        marginTop: 12,
      }}
    >
      <span>
        {result.created ? (
          <>
            Project <strong>{result.slug}</strong> created. The guide below holds its ingest key. Copy it now: the key is
            not shown again.
          </>
        ) : (
          <>
            Project <strong>{result.slug}</strong> already exists. Its guide has no ingest key: create one under the
            project&apos;s API Keys.
          </>
        )}
      </span>
      <textarea
        readOnly
        value={result.guide}
        aria-label="Setup guide"
        rows={12}
        onFocus={(e) => e.target.select()}
        style={{ fontFamily: 'monospace', fontSize: '0.75rem', padding: '0.3rem 0.5rem', resize: 'vertical' }}
      />
      <button type="button" className="btn btn-sm" onClick={onDismiss} style={{ alignSelf: 'flex-start' }}>
        Done
      </button>
    </div>
  )
}
