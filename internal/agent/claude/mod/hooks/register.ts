import type { Register } from 'claude-code'

// mako's Claude Code mod:
// 1. Live chat: streams each step of a turn (main conversation and subagents)
//    to the mako daemon over MAKO_LIVE_SOCKET.
// 2. Native agent messaging: long-polls the daemon over MAKO_LIVE_SOCKET for
//    incoming Mako teammate messages/notes/window events, delivers them in
//    strict FIFO order via $.prompt.submit, and acknowledges each batch only
//    after delivery succeeds (retrying on turn.complete if interrupted).
// 3. Live subagent status bar ($.ui.status) and /mako status command.

// The pieces gathered between sends, at most this long apart.
const BATCH_MS = 60

// Long-poll wait duration (kept safely under the 30s $.http.fetch limit).
const POLL_WAIT_SECONDS = 20

// Retry backoff after a transient transport error.
const POLL_ERROR_MS = 2000

// A piece: text, the start of a thinking block (its words stay here), a
// tool call begun, or a piece of the call's arguments as JSON.
type Piece = { k: 'text' | 'thinking' | 'tool' | 'input'; i: number; t?: string; n?: string }

type PollResponse = {
  has_messages?: boolean
  last_id?: number
  count?: number
  prompt?: string
  summary?: string
  has_user_prompt?: boolean
  user_prompt_id?: number
  user_prompt?: string
  child_status?: string
}

type StatusResponse = {
  summary?: string
  child_status?: string
}

type PendingDelivery = {
  lastId: number
  text: string
  summary: string
  toasted?: boolean
}

type PendingUserPrompt = {
  promptId: number
  text: string
}

type Creds = { session: string; socket: string; token: string }

let pollStopped = false
let pollRunning = false
let submittedThrough = 0
let pollTimer: { cancel(): void } | null = null
let deliverQueue: PendingDelivery[] = []
let delivering = false
let userPromptQueue: PendingUserPrompt[] = []
let deliveringUserPrompts = false
let waitingForTurnComplete = false
let lastChildStatus = ''
let liveCreds: Creds | null = null

async function readCreds($: any): Promise<Creds | null> {
  const session = await $.env.get('MAKO_SESSION')
  const socket = await $.env.get('MAKO_LIVE_SOCKET')
  const token = await $.env.get('MAKO_LIVE_TOKEN')
  if (!session || !socket || !token) {
    return null
  }
  return { session, socket, token }
}

function isRetryableSubmitReason(reason: string): boolean {
  return (
    reason.includes('the session was stopped and nobody has sent input since') ||
    reason.includes('was removed from the queue before it ran')
  )
}

function updateChildStatus($: any, nextStatus: string) {
  if (nextStatus === lastChildStatus) {
    return
  }
  lastChildStatus = nextStatus
  try {
    $.ui.status(nextStatus)
  } catch {
    // Non-interactive surface or unavailable status bar.
  }
}

async function ackMessages($: any, creds: Creds, ackThrough: number) {
  try {
    await $.http.fetch('http://mako/messages/ack', {
      method: 'POST',
      body: JSON.stringify({
        session: creds.session,
        token: creds.token,
        ack_through: ackThrough,
      }),
      socketPath: creds.socket,
    })
  } catch {
    // Daemon will keep it unacknowledged if unreachable.
  }
}

async function ackUserPrompt($: any, creds: Creds, promptId: number, ok: boolean, error?: string) {
  try {
    await $.http.fetch('http://mako/prompt/ack', {
      method: 'POST',
      body: JSON.stringify({
        session: creds.session,
        token: creds.token,
        prompt_id: promptId,
        ok,
        ...(error ? { error } : {}),
      }),
      socketPath: creds.socket,
    })
  } catch {
    // Daemon will time out waiting for ack if unreachable.
  }
}

async function drainUserPrompts($: any, creds: Creds) {
  if (deliveringUserPrompts || pollStopped) {
    return
  }
  deliveringUserPrompts = true
  try {
    while (!pollStopped && userPromptQueue.length > 0) {
      const head = userPromptQueue.shift()
      if (!head) {
        break
      }
      let reason = ''
      try {
        const result = await $.prompt.submit({ text: head.text })
        reason = result?.drop ?? ''
      } catch (err: any) {
        reason = String(err?.message ?? err ?? '')
      }
      await ackUserPrompt($, creds, head.promptId, !reason, reason)
    }
  } finally {
    deliveringUserPrompts = false
  }
}

async function drainDeliveries($: any, creds: Creds) {
  if (delivering || waitingForTurnComplete || pollStopped) {
    return
  }
  delivering = true
  try {
    while (!pollStopped && !waitingForTurnComplete && deliverQueue.length > 0) {
      const head = deliverQueue[0]
      if (!head) {
        break
      }
      if (head.summary && !head.toasted) {
        head.toasted = true
        try {
          $.ui.toast(head.summary)
        } catch {
          // Non-interactive surface or unavailable toast.
        }
      }
      let reason = ''
      try {
        const result = await $.prompt.submit({ text: head.text })
        reason = result?.drop ?? ''
      } catch (err: any) {
        reason = String(err?.message ?? err ?? '')
      }
      if (!reason) {
        deliverQueue.shift()
        await ackMessages($, creds, head.lastId)
        continue
      }
      if (isRetryableSubmitReason(reason)) {
        // Keep head at the front so later batches stay strictly behind it
        // and cannot ack past an undelivered message.
        waitingForTurnComplete = true
        break
      }
      // Permanent refusal (e.g. --max-turns): drop from local queue without acking.
      deliverQueue.shift()
    }
  } finally {
    delivering = false
  }
}

function scheduleNextPoll($: any, delayMs: number) {
  if (pollStopped) {
    return
  }
  pollTimer = $.clock.after(delayMs, () => {
    pollTimer = null
    void pollOnce($)
  })
}

async function pollOnce($: any) {
  if (pollStopped || pollRunning) {
    return
  }
  const creds = liveCreds
  if (!creds) {
    return
  }
  pollRunning = true
  let nextDelay = 50
  try {
    const res = await $.http.fetch('http://mako/messages/poll', {
      method: 'POST',
      body: JSON.stringify({
        session: creds.session,
        token: creds.token,
        after_id: submittedThrough,
        wait_seconds: POLL_WAIT_SECONDS,
      }),
      socketPath: creds.socket,
    })
    if (res.status === 404 || res.status === 403) {
      pollStopped = true
      return
    }
    if (!res.ok) {
      nextDelay = POLL_ERROR_MS
      return
    }
    const data = JSON.parse(res.text || '{}') as PollResponse
    updateChildStatus($, data.child_status ?? '')
    if (data.has_user_prompt && data.user_prompt && data.user_prompt_id) {
      nextDelay = 10
      userPromptQueue.push({
        promptId: data.user_prompt_id,
        text: data.user_prompt,
      })
      void drainUserPrompts($, creds)
    }
    if (data.has_messages && data.prompt && data.last_id && data.last_id > submittedThrough) {
      nextDelay = 10
      submittedThrough = data.last_id
      deliverQueue.push({
        lastId: data.last_id,
        text: data.prompt,
        summary: data.summary ?? 'Mako message received',
      })
      void drainDeliveries($, creds)
    }
  } catch {
    nextDelay = POLL_ERROR_MS
  } finally {
    pollRunning = false
    if (!pollStopped) {
      scheduleNextPoll($, nextDelay)
    }
  }
}

async function fetchMakoStatus($: any, creds: Creds): Promise<string> {
  try {
    const res = await $.http.fetch('http://mako/mako/status', {
      method: 'POST',
      body: JSON.stringify({ session: creds.session, token: creds.token }),
      socketPath: creds.socket,
    })
    if (!res.ok) {
      return `Mako status unavailable (HTTP ${res.status})`
    }
    const data = JSON.parse(res.text || '{}') as StatusResponse
    updateChildStatus($, data.child_status ?? '')
    return data.summary || 'No active Mako agents.'
  } catch (err: any) {
    return `Mako status error: ${String(err?.message ?? err ?? 'unreachable')}`
  }
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const creds = await readCreds($)
    if (creds) {
      liveCreds = creds
      pollStopped = false
      waitingForTurnComplete = false
      submittedThrough = 0
      deliverQueue = []
      userPromptQueue = []
      lastChildStatus = ''
      scheduleNextPoll($, 100)
      try {
        await $.command.register({
          name: 'mako',
          description: 'Show Mako project agents and pending messages',
        })
      } catch {
        // Ignore if /mako is already registered by another plugin.
      }
    }
    return next(e)
  })

  on('command.run', { command: 'mako' }, async ($, _e) => {
    const creds = liveCreds ?? (await readCreds($))
    if (!creds) {
      return { text: 'Mako session is not active.', isTextDisplayOnly: true }
    }
    const summary = await fetchMakoStatus($, creds)
    return { text: summary, isTextDisplayOnly: true }
  })

  on('session.end', async ($, e, next) => {
    pollStopped = true
    if (pollTimer) {
      pollTimer.cancel()
      pollTimer = null
    }
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    const creds = liveCreds
    if (creds && deliverQueue.length > 0) {
      waitingForTurnComplete = false
      void drainDeliveries($, creds)
    }
    return next(e)
  })

  on('turn.step', async function* ($, e, next) {
    const creds = await readCreds($)
    const stream = next(e)
    if (!creds) {
      return yield* stream
    }
    const { session, socket, token } = creds
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
