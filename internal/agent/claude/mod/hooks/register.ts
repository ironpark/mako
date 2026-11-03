import type { Register } from 'claude-code'

// mako's live chat: each step of the main conversation's turns, as its
// reply streams, goes to the mako daemon running the session, which shows
// it in the session's chat until the log has it. The pieces are passed on
// untouched, and a failure to send loses only the preview.

// The pieces gathered between sends, at most this long apart.
const BATCH_MS = 60

type Piece = { k: 'text' | 'tool'; i: number; t?: string; n?: string }

export const register: Register = on => {
  on('turn.step', async function* ($, e, next) {
    const session = await $.env.get('MAKO_SESSION')
    const socket = await $.env.get('MAKO_LIVE_SOCKET')
    const stream = next(e)
    if (e.agentId !== undefined || !session || !socket) {
      return yield* stream
    }
    const step = `${e.turnId}:${e.index}`
    let pieces: Piece[] = []
    let sent = 0
    // One send at a time, in order; one that fails is dropped.
    let queue: Promise<unknown> = Promise.resolve()
    const send = (end: boolean) => {
      const body = JSON.stringify({ session, step, end, pieces })
      pieces = []
      sent = Date.now()
      queue = queue
        .then(() => $.http.fetch('http://mako/live', { method: 'POST', body, socketPath: socket }))
        .catch(() => undefined)
    }
    send(false)
    try {
      for (;;) {
        const r = await stream.next()
        if (r.done) {
          return r.value
        }
        const c = r.value
        if (c.kind === 'text') {
          pieces.push({ k: 'text', i: c.index, t: c.text })
        } else if (c.kind === 'tool') {
          pieces.push({ k: 'tool', i: c.index, n: c.name })
        }
        if (pieces.length > 0 && Date.now() - sent >= BATCH_MS) {
          send(false)
        }
        yield c
      }
    } finally {
      send(true)
    }
  })
}
