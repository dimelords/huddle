import { expect, mock, test } from 'claude-code/testing'

const PANE = {
  plugin: 'huddle',
  component: 'Pane',
  requestId: 'huddle',
  props: {
    title: 'Huddle',
    isFocused: false,
    bodyColumns: 60,
    placement: 'dock',
    scroll: { offset: 0, bodyRows: 30 },
    view: {},
  },
} as const

const json = (v: unknown) => ({ value: { content: [{ type: 'text', text: JSON.stringify(v) }], isError: false } })

test('the pane shows the questions and who is active, and Svara asks Claude to answer', async ($, on) => {
  mock.clock(on)
  on('command.register', () => ({ value: { command: 'huddle' } }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))

  // No server is configured, so the usual names are tried: huddle-jobb is the one that answers.
  const calls: string[] = []
  on('mcp.call', (_, e) => {
    calls.push(`${e.server}/${e.tool}`)
    if (e.server !== 'huddle-jobb') {
      return { value: { content: [{ type: 'text', text: 'no such server' }], isError: true } }
    }
    switch (e.tool) {
      case 'whoami':
        return json({
          me: 'fredrik',
          signed_in_to_group: 'lidnass',
          groups: [{ id: 'lidnass', members: [
            { id: 'fredrik', display_name: 'Fredrik', active: true },
            { id: 'kumud', display_name: 'Kumud', active: true, last_seen_at: new Date().toISOString() },
            { id: 'daniel', display_name: 'Daniel', active: false, last_seen_at: new Date(Date.now() - 3 * 3600_000).toISOString() },
          ] }],
        })
      case 'inbox':
        expect(e.args.peek).toBe(true) // never marks anything as read
        return json({ events: [], awaiting_you: [{ id: 7, asker_id: 'kumud', answerer_id: 'fredrik', question: 'Which Postgres driver?', status: 'pending', thread_id: 7, created_at: new Date(Date.now() - 4 * 60_000).toISOString() }] })
      default:
        if (e.args.role === 'to_me') {
          return json({ questions: [{ id: 7, asker_id: 'kumud', answerer_id: 'fredrik', question: 'Which Postgres driver?', status: 'pending', thread_id: 7, created_at: new Date(Date.now() - 4 * 60_000).toISOString() }] })
        }
        return json({ questions: [
          { id: 9, asker_id: 'fredrik', answerer_id: 'daniel', question: 'Tabs or spaces?', status: 'draft', thread_id: 9 },
          { id: 8, asker_id: 'fredrik', answerer_id: 'kumud', question: 'Lunch?', status: 'approved', answer: 'Yes, at noon.', thread_id: 8,
            created_at: new Date(Date.now() - 20 * 60_000).toISOString(), decided_at: new Date(Date.now() - 10 * 60_000).toISOString() },
        ] })
    }
  })
  const submitted: string[] = []
  on('prompt.submit', (_, e) => {
    submitted.push(e.text)
    return { text: e.text }
  })

  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect(calls).toContain('huddle-jobb/inbox')
    expect(calls[0]).toBe('huddle/whoami') // tried first, and it does not answer
    expect((await ui.find({ type: 'Text', text: /^lidnass$/ }))?.text).toBe('lidnass')
    expect((await ui.find({ type: 'Text', text: /^Aktiv i gruppen/ }))?.text).toBe('Aktiv i gruppen lidnass, som Fredrik.')
    expect((await ui.find({ type: 'Text', text: /Kumud är här/ }))?.text).toBe('Kumud är här.')
    // Threads as cards: the one waiting for you first, your cards marked "Du".
    expect((await ui.find({ type: 'Text', text: /Samtal, 1 väntar på dig/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /Which Postgres driver\?/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /Inget utkast än/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /^Lunch\?$/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /Yes, at noon\./ }))?.text).toBeTruthy() // the answer, as its own card
    expect((await ui.find({ type: 'Text', text: /^Du$/ }))?.text).toBe('Du')
    expect((await ui.find({ type: 'Text', text: /Utkast skrivet, inte godkänt än/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /Daniel var här för 3 h sedan/ }))?.text).toBeTruthy()
    await ui.unmount()
  }

  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'answer7' })
  expect(submitted.at(-1)).toContain('fråga #7')
  expect(submitted.at(-1)).toContain('approve_answer')
  await ui.unmount()
})

test('the pane says so when the huddle server does not answer', { options: { server: 'huddle-jobb' } }, async ($, on) => {
  mock.clock(on)
  on('command.register', () => ({ value: { command: 'huddle' } }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('mcp.call', () => ({ value: { content: [{ type: 'text', text: 'no such server' }], isError: true } }))

  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  // The first refresh runs in the background; give it a moment to fail.
  let error: string | undefined
  for (let i = 0; i < 50 && !error; i++) {
    const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
    error = (await ui.find({ type: 'Text', text: /Huddle svarar inte/ }))?.text
    await ui.unmount()
    if (!error) for (let j = 0; j < 50; j++) await Promise.resolve()
  }
  expect(error).toContain('huddle-jobb')
})

