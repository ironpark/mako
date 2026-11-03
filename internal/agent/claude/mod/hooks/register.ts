import type { Register } from 'claude-code'

// mako's live chat: each step of a turn, the main conversation's and each
// subagent's, as its reply streams, goes to the mako daemon running the
// session, which shows it in the session's chat until the log has it. The
// pieces are passed on untouched, and a failure to send loses only the
// preview.

// The pieces gathered between sends, at most this long apart.
const BATCH_MS = 60

// A piece: text, the start of a thinking block (its words stay here), a
// tool call begun, or a piece of the call's arguments as JSON.
type Piece = { k: 'text' | 'thinking' | 'tool' | 'input'; i: number; t?: string; n?: string }

export const register: Register = on => {
  on('turn.step', async function* ($, e, next) {
    const session = await $.env.get('MAKO_SESSION')
    const socket = await $.env.get('MAKO_LIVE_SOCKET')
    const token = await $.env.get('MAKO_LIVE_TOKEN')
    const stream = next(e)
    if (!session || !socket || !token) {
      return yield* stream
    }
    const agent = e.agentId ?? ''
    const step = `${e.turnId}:${e.index}`
    let pieces: Piece[] = []
    let sent = 0
    const thinking = new Set<number>()
    // One send at a time, in order; one that fails is dropped.
    let queue: Promise<unknown> = Promise.resolve()
    const send = (end: boolean) => {
      const body = JSON.stringify({ session, token, agent, step, end, pieces })
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
        } else if (c.kind === 'thinking' && !thinking.has(c.index)) {
          thinking.add(c.index)
          pieces.push({ k: 'thinking', i: c.index })
        } else if (c.kind === 'tool') {
          pieces.push({ k: 'tool', i: c.index, n: c.name })
        } else if (c.kind === 'input') {
          pieces.push({ k: 'input', i: c.index, t: c.json })
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
