import { useEffect, useState } from 'react'

function formatBody(s: string | undefined): string {
  if (!s) return '{}'
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}

type StoredRequest = {
  id: string
  session_id: string
  model: string
  method: string
  path: string
  request: string
  response: string
  usage: { input_tokens: number; output_tokens: number }
  duration_ms: number
  status_code: number
  created_at: number
}

export default function SessionDetail({ sessionId, onBack }: { sessionId: string; onBack: () => void }) {
  const [requests, setRequests] = useState<StoredRequest[]>([])

  useEffect(() => {
    fetch(`/api/sessions/${sessionId}/requests?limit=50`)
      .then(r => r.json())
      .then(setRequests)
      .catch(console.error)
  }, [sessionId])

  const totalTokens = requests.reduce(
    (sum, r) => sum + r.usage.input_tokens + r.usage.output_tokens, 0
  )

  return (
    <div>
      <button onClick={onBack} style={{ marginBottom: '1rem' }}>&larr; Back</button>
      <h2>Session: {sessionId}</h2>
      <p>Total requests: {requests.length} | Total tokens: {totalTokens}</p>
      {requests.map(r => (
        <details key={r.id} style={{ marginBottom: '0.5rem' }}>
          <summary>{r.model} &mdash; {r.duration_ms}ms &mdash; {r.status_code}</summary>
          <pre>{formatBody(r.request)}</pre>
          <pre>{formatBody(r.response)}</pre>
        </details>
      ))}
    </div>
  )
}
