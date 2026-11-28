import { expect, mock, test } from 'claude-code/testing'

const TEST_ENV = {
  MAKO_SESSION: 'sess-1',
  MAKO_LIVE_SOCKET: '/tmp/live.sock',
  MAKO_LIVE_TOKEN: 'tok-1',
}

const END_INPUT = { reason: 'other', sessionId: 'sess-1', resume: false } as any

test('polls messages, updates child status bar, submits prompt, and acks in order', async ($, on) => {
  const clock = mock.clock(on)
  mock.env(on, TEST_ENV)

  const submitted: string[] = []
  const acked: number[] = []
  const toasts: string[] = []
  const statuses: string[] = []
  let pollCount = 0

  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', () => ({ sessionId: 'sess-1' }))
  on('command.register', () => ({ value: undefined }) as any)
  on('ui.toast', (_$, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.status', (_$, e) => {
    statuses.push(e.text ?? '')
    return { value: undefined }
  })
  on('prompt.submit', (_$, e) => {
    submitted.push(e.text)
    return { text: e.text }
  })
  on('http.fetch', (_$, e) => {
    if (String(e.url).endsWith('/messages/poll')) {
      pollCount++
      if (pollCount === 1) {
        return {
          value: {
            status: 200,
            ok: true,
            headers: {},
            text: JSON.stringify({
              has_messages: true,
              last_id: 7,
              count: 1,
              prompt: '[Mako message 7] build passed',
              summary: 'Mako message from worker',
              child_status: '1 subagent working',
            }),
          },
        }
      }
      return {
        value: {
          status: 200,
          ok: true,
          headers: {},
          text: JSON.stringify({ has_messages: false, child_status: '' }),
        },
      }
    }
    if (String(e.url).endsWith('/messages/ack')) {
      const body = JSON.parse(String(e.init?.body ?? '{}'))
      acked.push(body.ack_through)
      return { value: { status: 204, ok: true, headers: {}, text: '' } }
    }
    return { value: { status: 404, ok: false, headers: {}, text: '' } }
  })

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.advance(100)
  await clock.advance(60)
  await $.session.end(END_INPUT)

  expect(submitted).toEqual(['[Mako message 7] build passed'])
  expect(acked).toEqual([7])
  expect(toasts).toEqual(['Mako message from worker'])
  expect(statuses).toEqual(['1 subagent working', ''])
})

test('serializes FIFO delivery and retries interrupted submission on turn.complete without out-of-order ack', async ($, on) => {
  const clock = mock.clock(on)
  mock.env(on, TEST_ENV)

  const submitted: string[] = []
  const acked: number[] = []
  let refuseFirst = true
  let pollCount = 0

  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', () => ({ sessionId: 'sess-1' }))
  on('turn.complete', () => ({ text: '' }))
  on('command.register', () => ({ value: undefined }) as any)
  on('ui.toast', () => ({ value: undefined }))
  on('ui.status', () => ({ value: undefined }))
  on('prompt.submit', (_$, e) => {
    if (refuseFirst) {
      refuseFirst = false
      return { drop: 'the session was stopped and nobody has sent input since' }
    }
    submitted.push(e.text)
    return { text: e.text }
  })
  on('http.fetch', (_$, e) => {
    if (String(e.url).endsWith('/messages/poll')) {
      pollCount++
      if (pollCount === 1) {
        return {
          value: {
            status: 200,
            ok: true,
            headers: {},
            text: JSON.stringify({ has_messages: true, last_id: 1, prompt: 'msg-1', summary: 's1' }),
          },
        }
      }
      if (pollCount === 2) {
        return {
          value: {
            status: 200,
            ok: true,
            headers: {},
            text: JSON.stringify({ has_messages: true, last_id: 2, prompt: 'msg-2', summary: 's2' }),
          },
        }
      }
      return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify({ has_messages: false }) } }
    }
    if (String(e.url).endsWith('/messages/ack')) {
      acked.push(JSON.parse(String(e.init?.body ?? '{}')).ack_through)
      return { value: { status: 204, ok: true, headers: {}, text: '' } }
    }
    return { value: { status: 404, ok: false, headers: {}, text: '' } }
  })

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  // Poll 1 gets msg-1 (refused on submit -> paused) and poll 2 queues msg-2 behind msg-1.
  await clock.advance(100)
  await clock.advance(60)

  // Neither msg-1 nor msg-2 should be acked before turn.complete.
  expect(submitted).toEqual([])
  expect(acked).toEqual([])

  // turn.complete drains msg-1 then msg-2 in strict order.
  await $.turn.complete({ turnId: 't1', answer: 'ok', durationMs: 10, isAborted: false, reason: 'end_turn' } as any)
  await clock.settle()
  await $.session.end(END_INPUT)

  expect(submitted).toEqual(['msg-1', 'msg-2'])
  expect(acked).toEqual([1, 2])
})

test('/mako command returns display-only project status', async ($, on) => {
  const clock = mock.clock(on)
  mock.env(on, TEST_ENV)

  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', () => ({ sessionId: 'sess-1' }))
  on('command.register', () => ({ value: undefined }) as any)
  on('ui.status', () => ({ value: undefined }))
  on('http.fetch', (_$, e) => {
    if (String(e.url).endsWith('/mako/status')) {
      return {
        value: {
          status: 200,
          ok: true,
          headers: {},
          text: JSON.stringify({
            summary: 'Mako session sess-1 (/work) · 0 pending message(s)',
            child_status: '2 subagents working',
          }),
        },
      }
    }
    return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify({ has_messages: false }) } }
  })

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  const ans = await $.command.run({ command: 'mako', args: '' } as any)
  await $.session.end(END_INPUT)
  await clock.settle()

  expect(ans.text).toBe('Mako session sess-1 (/work) · 0 pending message(s)')
  expect(ans.isTextDisplayOnly).toBe(true)
})

test('forwards prompt.suggest ghost text to daemon and clears on prompt.submit and turn.start', async ($, on) => {
  const clock = mock.clock(on)
  mock.env(on, TEST_ENV)

  const suggestions: string[] = []
  let showNext = true

  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', () => ({ sessionId: 'sess-1' }))
  on('command.register', () => ({ value: undefined }) as any)
  on('ui.status', () => ({ value: undefined }))
  on('prompt.suggest', () => ({ isShown: showNext }))
  on('prompt.submit', (_$, e) => ({ text: e.text }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('http.fetch', (_$, e) => {
    if (String(e.url).endsWith('/suggestion')) {
      const body = JSON.parse(String(e.init?.body ?? '{}'))
      suggestions.push(body.text)
      return { value: { status: 204, ok: true, headers: {}, text: '' } }
    }
    return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify({ has_messages: false }) } }
  })

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })

  await $.prompt.suggest({ text: 'Run the unit tests' } as any)
  await clock.settle()
  expect(suggestions).toEqual(['Run the unit tests'])

  // Suppressed suggestion (isShown: false) is not forwarded.
  showNext = false
  await $.prompt.suggest({ text: 'Should not show' } as any)
  await clock.settle()
  expect(suggestions).toEqual(['Run the unit tests'])

  // Submitting a prompt or starting a turn clears ghost text.
  await $.prompt.submit({ text: 'Run the unit tests' } as any)
  await clock.settle()
  expect(suggestions).toEqual(['Run the unit tests', ''])

  showNext = true
  await $.prompt.suggest({ text: 'Commit the changes' } as any)
  await clock.settle()
  await $.turn.start({ turnId: 't2' } as any)
  await clock.settle()
  expect(suggestions).toEqual(['Run the unit tests', '', 'Commit the changes', ''])

  await $.session.end(END_INPUT)
})

test('sends suggestion updates in order and skips repeats', async ($, on) => {
  const clock = mock.clock(on)
  mock.env(on, TEST_ENV)

  const arrived: string[] = []
  let release: () => void = () => {}
  const slow = new Promise<void>(resolve => {
    release = resolve
  })

  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', () => ({ sessionId: 'sess-1' }))
  on('command.register', () => ({ value: undefined }) as any)
  on('ui.status', () => ({ value: undefined }))
  on('prompt.suggest', () => ({ isShown: true }))
  on('prompt.submit', (_$, e) => ({ text: e.text }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('http.fetch', async (_$, e) => {
    if (String(e.url).endsWith('/suggestion')) {
      const body = JSON.parse(String(e.init?.body ?? '{}'))
      if (body.text) {
        await slow
      }
      arrived.push(body.text)
      return { value: { status: 204, ok: true, headers: {}, text: '' } }
    }
    return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify({ has_messages: false }) } }
  })

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })

  // The clear waits behind the slow suggestion instead of overtaking it.
  await $.prompt.suggest({ text: 'Run the unit tests' } as any)
  await $.prompt.submit({ text: 'Run the unit tests' } as any)
  await clock.settle()
  expect(arrived).toEqual([])
  release()
  await clock.settle()
  expect(arrived).toEqual(['Run the unit tests', ''])

  // A clear with nothing shown, and the same suggestion twice, send nothing new.
  await $.turn.start({ turnId: 't2' } as any)
  await $.prompt.suggest({ text: 'Commit the changes' } as any)
  await $.prompt.suggest({ text: 'Commit the changes' } as any)
  await clock.settle()
  expect(arrived).toEqual(['Run the unit tests', '', 'Commit the changes'])

  await $.session.end(END_INPUT)
})

test('a subagent turn keeps the suggestion, and one offered before a newer turn is not sent', async ($, on) => {
  const clock = mock.clock(on)
  mock.env(on, TEST_ENV)

  const arrived: string[] = []
  let offer: () => void = () => {}
  const offered = new Promise<void>(resolve => {
    offer = resolve
  })
  let gate = false

  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', () => ({ sessionId: 'sess-1' }))
  on('command.register', () => ({ value: undefined }) as any)
  on('ui.status', () => ({ value: undefined }))
  on('prompt.suggest', async () => {
    if (gate) {
      await offered
    }
    return { isShown: true }
  })
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('http.fetch', (_$, e) => {
    if (String(e.url).endsWith('/suggestion')) {
      arrived.push(JSON.parse(String(e.init?.body ?? '{}')).text)
      return { value: { status: 204, ok: true, headers: {}, text: '' } }
    }
    return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify({ has_messages: false }) } }
  })

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })

  await $.prompt.suggest({ text: 'Run the unit tests' } as any)
  await $.turn.start({ turnId: 'sub-1', agentId: 'agent-1' } as any)
  await clock.settle()
  expect(arrived).toEqual(['Run the unit tests'])

  // The suggestion settles only after the main turn started: it is stale.
  gate = true
  const pending = $.prompt.suggest({ text: 'Commit the changes' } as any)
  await $.turn.start({ turnId: 't2' } as any)
  offer()
  await pending
  await clock.settle()
  expect(arrived).toEqual(['Run the unit tests', ''])

  await $.session.end(END_INPUT)
})

test('a suggestion update the daemon refused goes out again', async ($, on) => {
  const clock = mock.clock(on)
  mock.env(on, TEST_ENV)

  const sent: string[] = []
  let refuse = true

  on('session.start', () => ({ cwd: '/work' }))
  on('session.end', () => ({ sessionId: 'sess-1' }))
  on('command.register', () => ({ value: undefined }) as any)
  on('ui.status', () => ({ value: undefined }))
  on('prompt.suggest', () => ({ isShown: true }))
  on('prompt.submit', (_$, e) => ({ text: e.text }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('http.fetch', (_$, e) => {
    if (String(e.url).endsWith('/suggestion')) {
      sent.push(JSON.parse(String(e.init?.body ?? '{}')).text)
      if (refuse) {
        return { value: { status: 503, ok: false, headers: {}, text: '' } }
      }
      return { value: { status: 204, ok: true, headers: {}, text: '' } }
    }
    return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify({ has_messages: false }) } }
  })

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })

  await $.prompt.suggest({ text: 'Run the unit tests' } as any)
  await clock.settle()
  // The refused suggestion is not taken as delivered: the same one again is sent.
  refuse = false
  await $.prompt.suggest({ text: 'Run the unit tests' } as any)
  await clock.settle()
  expect(sent).toEqual(['Run the unit tests', 'Run the unit tests'])

  // A delivered one is not sent twice.
  await $.prompt.suggest({ text: 'Run the unit tests' } as any)
  await clock.settle()
  expect(sent).toEqual(['Run the unit tests', 'Run the unit tests'])

  await $.session.end(END_INPUT)
})
