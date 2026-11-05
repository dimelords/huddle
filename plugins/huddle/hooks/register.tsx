import { atom, read, update } from 'claude-code'
import type { EngineInterface, McpToolResult, Register } from 'claude-code'

import type { HuddleImage, HuddleMember, HuddleQuestion, HuddleView } from '../types'

// A pane for huddle (https://github.com/dimelords/huddle): who is in the
// huddle right now, the questions waiting for you, and where your own
// questions stand in the approval. It reads through the session's own huddle
// MCP connection ($.mcp.call), so an OAuth sign-in needs no extra token, and
// it only peeks at the inbox: nothing is marked as read.

const PANE = 'huddle'
const POLL_MS = 20_000
const DEFAULT_SERVER = '' // none: try CANDIDATES in order
const CANDIDATES = ['huddle', 'huddle-jobb', 'huddle-privat', 'huddle-railway']

// Mid tones that read on light and dark panes alike; white initials on top.
const FACES = ['#1F6F78', '#5E7F34', '#74488A', '#B07A22', '#4A5A73']
const HERE = '#2FA37A' // the ring of someone active right now
const STEP_ON = '#1F6F78'
const STEP_OFF = '#9AA3AD'

const EMPTY: HuddleView = {
  server: '',
  me: '',
  meName: '',
  group: '',
  groupName: '',
  locked: false,
  groups: [],
  members: [],
  names: {},
  awaiting: [],
  asked: [],
  toMe: [],
  error: '',
  updatedAt: 0,
}

const view = atom({ plugin: 'huddle', key: 'view' } as const, EMPTY)
const seen = atom({ plugin: 'huddle', key: 'seen' } as const, [] as number[])
// Texts shown whole, sections shown in full, and questions Claude is answering.
const expanded = atom({ plugin: 'huddle', key: 'expanded' } as const, [] as string[])
const showAll = atom({ plugin: 'huddle', key: 'showAll' } as const, [] as string[])
const working = atom({ plugin: 'huddle', key: 'working' } as const, [] as number[])
const reviewing = atom({ plugin: 'huddle', key: 'reviewing' } as const, [] as { id: number; at: number }[])
const thumbs = atom({ plugin: 'huddle', key: 'thumbs' } as const, {} as Record<string, string>)
const THUMBS_PER_POLL = 6 // thumbnails fetched per refresh; the rest come on the next ones
const THUMB_W = 240 // widest a picture is drawn on a card, CSS pixels
const REVIEW_MS = 60_000 // the "Granska utkastet" button comes back after this

const RECENT_CARDS = 4 // cards shown per thread before "Visa äldre"
const KEEP_MS = 7 * 24 * 3600_000 // decided questions older than this drop out of the pane

// ------------------------------------------------------------------ data

// The JSON a huddle tool answered with: its structured result, or its text.
function payload(res: McpToolResult): any {
  if (res.isError) {
    throw new Error(res.content.map(c => c.text ?? '').join(' ').trim() || 'huddle answered with an error')
  }
  if (res.structuredContent !== undefined) return res.structuredContent
  const text = res.content.find(c => c.type === 'text')?.text
  return text ? JSON.parse(text) : {}
}

function picture(a: any): HuddleImage {
  return { id: String(a.id), name: String(a.name ?? 'bild'), width: Number(a.width) || 1, height: Number(a.height) || 1 }
}

function question(q: any): HuddleQuestion {
  return {
    id: Number(q.id),
    from: String(q.asker_id ?? ''),
    to: String(q.answerer_id ?? ''),
    text: String(q.question ?? ''),
    status: String(q.status ?? ''),
    threadId: Number(q.thread_id ?? q.id),
    createdAt: q.created_at,
    answer: q.answer ?? undefined,
    declineReason: q.decline_reason ?? undefined,
    decidedAt: q.decided_at ?? undefined,
    images: (q.attachments ?? []).map(picture),
    answerImages: (q.answer_attachments ?? []).map(picture),
  }
}

// The pane opens at the start and shows what huddle says; it keeps the
// server that answered, so later polls go straight to it.
async function refresh($: EngineInterface, configured: string): Promise<void> {
  const current = await read($, view)
  // A configured server always wins over the one remembered from before.
  const servers = configured ? [configured] : current.server ? [current.server] : CANDIDATES
  const errors: string[] = []
  for (const server of servers) {
    try {
      const who = payload(await $.mcp.call(server, 'whoami'))
      const inbox = payload(await $.mcp.call(server, 'inbox', { peek: true, limit: 20 }))
      const mine = payload(await $.mcp.call(server, 'list_questions', { role: 'asked', limit: 40 }))
      const theirs = payload(await $.mcp.call(server, 'list_questions', { role: 'to_me', limit: 40 }))

      const groups: any[] = who.groups ?? []
      const group: string = who.signed_in_to_group || (groups.length === 1 ? groups[0].id : '')
      const me = String(who.me ?? '')
      const names: Record<string, string> = {}
      const members = new Map<string, HuddleMember>()
      for (const g of groups) {
        for (const m of g.members ?? []) {
          names[m.id] = m.display_name || m.id
          if (m.id === me) continue
          members.set(m.id, { id: m.id, name: m.display_name || m.id, active: !!m.active, lastSeenAt: m.last_seen_at })
        }
      }
      const awaiting = (inbox.awaiting_you ?? []).map(question)
      // Open questions, and decided ones from the last week.
      const recent = (q: HuddleQuestion) =>
        q.status === 'pending' || q.status === 'draft' || !q.decidedAt || Date.now() - Date.parse(q.decidedAt) < KEEP_MS
      const asked = (mine.questions ?? []).map(question).filter(recent)
      const toMe = (theirs.questions ?? []).map(question).filter(recent)
      // A question stops being "Claude is answering" once it has a draft or is gone.
      await update($, working, ids => ids.filter(id => awaiting.some((q: HuddleQuestion) => q.id === id && q.status === 'pending')))

      const known = await read($, seen)
      const fresh = awaiting.filter((q: HuddleQuestion) => !known.includes(q.id))
      if (fresh.length > 0 && current.updatedAt > 0) {
        $.ui.toast(fresh.length === 1 ? `${names[fresh[0].from] ?? fresh[0].from} frågar dig något i Huddle` : `${fresh.length} nya frågor till dig i Huddle`)
      }
      await update($, seen, () => awaiting.map((q: HuddleQuestion) => q.id))
      $.ui.status(awaiting.length > 0 ? `Huddle: ${awaiting.length} väntar på dig` : undefined)

      await update($, view, () => ({
        server,
        me,
        meName: names[me] ?? me,
        group,
        groupName: String(groups.find(g => g.id === group)?.name || group),
        locked: !!who.signed_in_to_group,
        groups: groups.map(g => String(g.id)),
        members: [...members.values()].sort((a, b) => Number(b.active) - Number(a.active) || a.name.localeCompare(b.name)),
        names,
        awaiting,
        asked,
        toMe,
        error: '',
        updatedAt: Date.now(),
      }))
      await fetchThumbs($, server, [...asked, ...toMe])
      return
    } catch (err) {
      errors.push(`${server}: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
  await update($, view, v => ({
    ...v,
    server: '',
    error: `Huddle svarar inte. ${errors.join(' ')}${current.server || configured ? '' : ' Heter servern något annat, sätt pluginConfigs.huddle.options.server i ~/.claude/settings.json.'}`,
  }))
}

// Thumbnails for the images on the cards, a few per refresh, kept only while
// a card shows them. One that fails is drawn as its name alone.
async function fetchThumbs($: EngineInterface, server: string, qs: HuddleQuestion[]): Promise<void> {
  const ids = [...new Set(qs.flatMap(q => [...q.images, ...q.answerImages].map(a => a.id)))]
  const have = await read($, thumbs)
  const got: Record<string, string> = {}
  for (const id of ids.filter(id => !(id in have)).slice(0, THUMBS_PER_POLL)) {
    try {
      const res = payload(await $.mcp.call(server, 'get_attachment', { attachment_id: id, thumbnail: true }))
      if (typeof res.thumbnail_base64 === 'string' && res.thumbnail_base64) got[id] = res.thumbnail_base64
    } catch {
      // Not allowed or gone: the card shows the file name.
    }
  }
  const kept = Object.keys(have).filter(id => ids.includes(id))
  if (Object.keys(got).length > 0 || kept.length !== Object.keys(have).length) {
    await update($, thumbs, cur => Object.fromEntries([...kept.map(id => [id, cur[id]!] as const), ...Object.entries(got)]))
  }
}

// ------------------------------------------------------------------ words

function ago(iso: string | undefined, now: number): string {
  if (!iso) return 'aldrig'
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000))
  if (s < 90) return 'nyss'
  if (s < 3600) return `för ${Math.round(s / 60)} min sedan`
  if (s < 86_400) return `för ${Math.round(s / 3600)} h sedan`
  return `för ${Math.round(s / 86_400)} d sedan`
}

function short(text: string, n = 220): string {
  const one = text.replace(/\s+/g, ' ').trim()
  return one.length > n ? `${one.slice(0, n - 1)}…` : one
}

function firstName(name: string): string {
  return name.split(/\s+/)[0] || name
}

// "Kumud", "Kumud och Daniel", "Kumud, Daniel och Testkollega"
function list(names: string[]): string {
  if (names.length <= 1) return names.join('')
  return `${names.slice(0, -1).join(', ')} och ${names[names.length - 1]}`
}

// where says which group the session acts in, and as whom.
function where(v: HuddleView): string {
  const as = `som ${v.meName || v.me}`
  if (v.locked) return `Aktiv i gruppen ${v.groupName || v.group}, ${as}.`
  if (v.group) return `Gruppen ${v.groupName || v.group}, ${as}.`
  return `Inte låst till en grupp: du ser ${v.groups.join(', ')}, ${as}.`
}

function presence(members: HuddleMember[]): string {
  const here = members.filter(m => m.active).map(m => firstName(m.name))
  if (members.length === 0) return 'Bara du i gruppen än.'
  if (here.length === 0) return 'Ingen annan är här just nu.'
  return `${list(here)} är här.`
}

// How far a question has come: asked (1), drafted (2), approved (3).
function stage(q: HuddleQuestion): { step: number; mine: string; theirs: string } {
  if (q.status === 'draft') return { step: 2, mine: 'Utkastet väntar på dig', theirs: 'Utkast skrivet, inte godkänt än' }
  if (q.status === 'approved') return { step: 3, mine: 'Besvarad', theirs: 'Besvarad' }
  if (q.status === 'declined') return { step: 3, mine: 'Avböjd', theirs: 'Avböjd' }
  return { step: 1, mine: 'Inget utkast än', theirs: 'Inget svar än' }
}

// ------------------------------------------------------------------ drawings

function esc(s: string): string {
  return s.replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c] ?? c)
}

function initials(name: string): string {
  const parts = name.trim().split(/[\s._-]+/).filter(Boolean)
  return ((parts[0]?.[0] ?? '?') + (parts.length > 1 ? parts[parts.length - 1]![0] : '')).toUpperCase()
}

function hue(id: string): string {
  let h = 0
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0
  return FACES[h % FACES.length]!
}

// The huddle itself: one round face per colleague, ringed while active. Its
// size is passed to the Svg element too: without it the pane stretches the
// drawing to its full width.
function faces(members: HuddleMember[]): { source: string; width: number; height: number } {
  const shown = members.slice(0, 8)
  const r = 14 // face radius
  const pad = 5 // room for the ring and its stroke
  const step = 2 * (r + pad) + 2
  const width = Math.max(1, shown.length) * step
  const height = 2 * (r + pad)
  const cy = r + pad
  const body = shown
    .map((m, i) => {
      const cx = i * step + r + pad
      const ring = m.active ? `<circle cx="${cx}" cy="${cy}" r="${r + 3}" fill="none" stroke="${HERE}" stroke-width="2"/>` : ''
      return `${ring}<circle cx="${cx}" cy="${cy}" r="${r}" fill="${hue(m.id)}" opacity="${m.active ? 1 : 0.55}"/>` +
        `<text x="${cx}" y="${cy + 4}" text-anchor="middle" font-family="-apple-system, system-ui, sans-serif" font-size="11" font-weight="600" fill="#fff">${esc(initials(m.name))}</text>`
    })
    .join('')
  return {
    source: `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">${body}</svg>`,
    width,
    height,
  }
}

// A thumbnail as an SVG image, sized to the picture's own shape.
function thumbSvg(b64: string, a: HuddleImage): { source: string; width: number; height: number } {
  const width = Math.min(THUMB_W, a.width)
  const height = Math.max(1, Math.round((width * a.height) / a.width))
  return {
    source: `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}"><image href="data:image/jpeg;base64,${b64}" width="${width}" height="${height}" preserveAspectRatio="xMidYMid meet"/></svg>`,
    width,
    height,
  }
}

// The short vertical line that ties a reply to the card above it.
const LINK = `<svg xmlns="http://www.w3.org/2000/svg" width="2" height="12" viewBox="0 0 2 12"><line x1="1" y1="0" x2="1" y2="12" stroke="${STEP_OFF}" stroke-width="2"/></svg>`

// Asked, drafted, approved: three stops on one line, filled up to `step`.
function steps(step: number): string {
  const xs = [6, 36, 66]
  const line = (a: number, b: number, on: boolean) =>
    `<line x1="${xs[a]! + 5}" y1="7" x2="${xs[b]! - 5}" y2="7" stroke="${on ? STEP_ON : STEP_OFF}" stroke-width="2" stroke-linecap="round"/>`
  const dot = (i: number) =>
    i < step
      ? `<circle cx="${xs[i]}" cy="7" r="4.5" fill="${STEP_ON}"/>`
      : `<circle cx="${xs[i]}" cy="7" r="4" fill="none" stroke="${STEP_OFF}" stroke-width="1.5"/>`
  return `<svg xmlns="http://www.w3.org/2000/svg" width="72" height="14" viewBox="0 0 72 14">${line(0, 1, step >= 2)}${line(1, 2, step >= 3)}${dot(0)}${dot(1)}${dot(2)}</svg>`
}

// ------------------------------------------------------------------ hooks

export const register: Register = (on, options) => {
  // The server's name as /mcp lists it; unset, the usual names are tried.
  const configured = String((options as { server?: unknown } | undefined)?.server ?? '').trim() || DEFAULT_SERVER

  on('session.start', async ($, e, next) => {
    await $.command.register({ name: 'huddle', description: 'Visa Huddle-panelen: vem som är här, frågor till dig och dina frågor' })

    void refresh($, configured)
    $.clock.every(POLL_MS, () => void refresh($, configured))
    void $.ui.open({ id: PANE, title: 'Huddle' })

    return next(e)
  })

  on('command.run', { command: 'huddle' }, async $ => {
    await $.ui.open({ id: PANE, title: 'Huddle' })

    return { text: 'Huddle-panelen är öppen.' }
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const els = $.ui.resolve(e)
    const { Box, Text, Button } = els
    const Svg = 'Svg' in els ? els.Svg : undefined
    const v = await read($, view)
    const open = await read($, expanded)
    const full = await read($, showAll)
    const busy = await read($, working)
    const pics = await read($, thumbs)
    const inReview = (await read($, reviewing)).filter(r => Date.now() - r.at < REVIEW_MS).map(r => r.id)
    const now = Date.now()
    const ask = (text: string) => () => void $.prompt.submit({ text })
    const name = (id: string) => firstName(v.names[id] ?? id)

    const retry = <Button key="retry" label="Försök igen" onPress={() => void refresh($, configured)} />

    if (v.updatedAt === 0) {
      return (
        <Box flexDirection="column" gap="sm" padding="sm">
          <Text size="heading" weight="semibold">Huddle</Text>
          <Text color="subtle">{v.error || 'Ansluter till Huddle…'}</Text>
          {v.error && retry}
        </Box>
      )
    }

    // A text shortened, with a button that shows all of it. A Button may only
    // hold strings and Text (anything else makes the engine refuse the whole
    // drawing), and a desktop draws even a plain one as a button, so the text
    // stays Text and the toggle is its own button.
    const clip = (key: string, text: string, what: string) => {
      const whole = text.trim()
      const clipped = short(whole)
      if (clipped === whole.replace(/\s+/g, ' ')) return <Text>{whole}</Text>
      const isOpen = open.includes(key)
      const toggle = () => void update($, expanded, keys => (keys.includes(key) ? keys.filter(k => k !== key) : [...keys, key]))
      return (
        <Box flexDirection="column" gap="xs" alignItems="flex-start">
          <Text>{isOpen ? whole : clipped}</Text>
          <Button key={`more-${key}`} plain label={isOpen ? 'Visa mindre' : `Visa hela ${what}`} onPress={toggle} />
        </Box>
      )
    }

    // Threads, as cards in the order they were written: each question, then
    // its answer. Your own cards sit at the left and everyone else's are
    // indented, joined by a short line. A question still open gets a card
    // where its answer will go. Threads waiting for you come first, then the
    // most recently active.
    type Card = {
      key: string
      author: string // member id
      text: string
      at?: string
      question?: HuddleQuestion // set on a question's card: its status shows at the bottom
      images: HuddleImage[]
    }
    const threads = (() => {
      const byThread = new Map<number, HuddleQuestion[]>()
      for (const q of [...v.asked, ...v.toMe]) {
        if (!byThread.get(q.threadId)?.some(o => o.id === q.id)) byThread.set(q.threadId, [...(byThread.get(q.threadId) ?? []), q])
      }
      const isOpen = (q: HuddleQuestion) => q.status === 'pending' || q.status === 'draft'
      return [...byThread.entries()]
        .map(([id, qs]) => {
          qs.sort((a, b) => Date.parse(a.createdAt ?? '') - Date.parse(b.createdAt ?? '') || a.id - b.id)
          const cards: Card[] = []
          for (const q of qs) {
            cards.push({ key: `q${q.id}`, author: q.from, text: q.text, at: q.createdAt, question: q, images: q.images })
            if (q.status === 'approved' && q.answer) cards.push({ key: `a${q.id}`, author: q.to, text: q.answer, at: q.decidedAt, images: q.answerImages })
          }
          const latest = Math.max(...qs.map(q => Date.parse(q.decidedAt ?? q.createdAt ?? '') || 0))
          const waitsForMe = qs.some(q => q.to === v.me && isOpen(q))
          return { id, cards, latest, waitsForMe }
        })
        .sort((a, b) => Number(b.waitsForMe) - Number(a.waitsForMe) || b.latest - a.latest)
    })()
    const waitingForMe = v.toMe.filter(q => q.status === 'pending' || q.status === 'draft').length

    const review = (q: HuddleQuestion) => async () => {
      await update($, reviewing, list => [...list.filter(r => r.id !== q.id && Date.now() - r.at < REVIEW_MS), { id: q.id, at: Date.now() }])
      $.ui.toast('Godkännandeformuläret öppnas i chatten')
      await $.prompt.submit({ text: `Kör approve_answer för huddle-fråga #${q.id} så att jag kan granska och godkänna utkastet.` })
    }

    const answer = (q: HuddleQuestion) => async () => {
      await update($, working, ids => (ids.includes(q.id) ? ids : [...ids, q.id]))
      $.ui.toast(`Claude svarar ${name(q.from)} i chatten`)
      await $.prompt.submit({
        text: `Läs huddle-tråden för fråga #${q.id} med thread, svara med draft_answer utifrån det du vet i den här sessionen. Kräver gruppen godkännande sparas bara ett utkast: visa mig det och kör sedan approve_answer. Annars skickas svaret direkt: säg på en rad vad du skickade.`,
      })
    }

    // Pictures on a card: the thumbnail where the surface draws Svg and it has
    // arrived, the file name always, so the card says what is attached.
    const gallery = (c: Card) =>
      c.images.length > 0 && (
        <Box flexDirection="column" gap="xs">
          {c.images.map(a => {
            const b64 = pics[a.id]
            const t = b64 && Svg ? thumbSvg(b64, a) : undefined
            return (
              <Box key={`img-${c.key}-${a.id}`} flexDirection="column" gap="xs">
                {t && Svg && <Svg source={t.source} alt={`Bild: ${a.name}`} width={t.width} height={t.height} />}
                <Text size="footnote" color="subtle">{`🖼 ${a.name}`}</Text>
              </Box>
            )
          })}
        </Box>
      )

    const progress = (q: HuddleQuestion, mine: boolean) => {
      const s = stage(q)
      return (
        <Box flexDirection="row" alignItems="center" gap="sm">
          {Svg ? <Svg source={steps(s.step)} alt={`Steg ${s.step} av 3: frågad, utkast, godkänd`} width={72} height={14} /> : <Text color="subtle" tabular>{'●'.repeat(s.step)}{'○'.repeat(3 - s.step)}</Text>}
          <Text size="footnote" color="subtle">{mine ? s.mine : s.theirs}</Text>
        </Box>
      )
    }

    return (
      <Box flexDirection="column" gap="lg" padding="sm">
        <Box flexDirection="column" gap="xs">
          <Text size="title" weight="bold">{v.groupName || v.group || (v.groups.length > 1 ? `${v.groups.length} grupper` : 'Huddle')}</Text>
          {v.me && <Text size="footnote" color="subtle">{where(v)}</Text>}
          {v.members.length > 0 && Svg && (() => {
            const f = faces(v.members)
            return <Svg source={f.source} width={f.width} height={f.height} alt={v.members.map(m => `${m.name}${m.active ? ' (här)' : ''}`).join(', ')} />
          })()}
          <Text color="subtle">{presence(v.members)}</Text>
        </Box>

        {v.error && (
          <Box flexDirection="column" gap="xs">
            <Text color="warning">{v.error}</Text>
            {retry}
          </Box>
        )}

        <Box flexDirection="column" gap="lg">
          <Text size="heading" weight="semibold">{waitingForMe > 0 ? `Samtal, ${waitingForMe} väntar på dig` : 'Samtal'}</Text>
          {threads.length === 0 && (
            <Text color="subtle">Inga samtal än. Be Claude fråga någon, till exempel ”fråga {v.members[0] ? firstName(v.members[0].name) : 'en kollega'} om …”.</Text>
          )}
          {threads.map(t => {
            const key = `t${t.id}`
            const isFull = full.includes(key)
            const older = t.cards.length - RECENT_CARDS
            const shown = isFull || older <= 0 ? t.cards : t.cards.slice(-RECENT_CARDS)
            const toggle = () => void update($, showAll, ids => (ids.includes(key) ? ids.filter(x => x !== key) : [...ids, key]))
            return (
              <Box key={key} flexDirection="column">
                {older > 0 && (
                  <Box marginBottom="xs">
                    <Button key={`older-${key}`} plain dimColor label={isFull ? 'Dölj äldre' : `${older} äldre i tråden`} onPress={toggle} />
                  </Box>
                )}
                {shown.map((c, i) => {
                  const mine = c.author === v.me
                  const q = c.question
                  const open = q && (q.status === 'pending' || q.status === 'draft')
                  const forMe = !!open && q!.to === v.me
                  const person = v.members.find(m => m.id === c.author)
                  // The line between cards sits just inside the indent, where both an
                  // own card (full width) and an indented one are, so it always runs
                  // from the card above to the card below and never hangs in the air.
                  return (
                    <Box key={c.key} flexDirection="column">
                      {i > 0 && (
                        <Box marginLeft="xl">
                          <Box marginLeft="md">
                            {Svg ? <Svg source={LINK} alt="nästa i tråden" width={2} height={12} /> : <Text color="subtle">│</Text>}
                          </Box>
                        </Box>
                      )}
                      <Box flexDirection="column" gap="xs" padding="sm" marginLeft={mine ? 0 : 'xl'} borderStyle="round" borderColor={forMe ? 'suggestion' : 'subtle'}>
                        <Box flexDirection="row" alignItems="center" gap="sm" flexWrap="wrap">
                          <Text weight="semibold">{mine ? 'Du' : person?.active ? `● ${name(c.author)}` : name(c.author)}</Text>
                          {c.at && <Text size="footnote" color="subtle">{ago(c.at, now)}</Text>}
                        </Box>
                        {clip(c.key, c.text, q ? 'frågan' : 'svaret')}
                        {gallery(c)}

                        {q?.status === 'declined' && (
                          <Text size="footnote" color="warning">
                            {q.to === v.me ? 'Du avböjde' : `${name(q.to)} avböjde`}{q.declineReason ? `: ${q.declineReason}` : '.'}
                          </Text>
                        )}
                        {open && (
                          <Box flexDirection="row" alignItems="center" gap="md" flexWrap="wrap">
                            {forMe &&
                              (q!.status === 'draft' && inReview.includes(q!.id) ? (
                                <Text size="footnote" color="suggestion">Formuläret öppnas i chatten. Välj approve och tryck Accept där.</Text>
                              ) : q!.status === 'draft' ? (
                                <Button key={`approve${q!.id}`} variant="primary" label="Granska utkastet" onPress={review(q!)} />
                              ) : busy.includes(q!.id) ? (
                                <Text size="footnote" color="suggestion">Claude skriver ett utkast i chatten …</Text>
                              ) : (
                                <Button key={`answer${q!.id}`} variant="primary" label="Svara" onPress={answer(q!)} />
                              ))}
                            {progress(q!, forMe)}
                          </Box>
                        )}
                      </Box>
                    </Box>
                  )
                })}
              </Box>
            )
          })}
        </Box>

        {v.members.length > 0 && (
          <Box flexDirection="column" gap="xs">
            {v.members.filter(m => !m.active).map(m => (
              <Text key={`m${m.id}`} size="footnote" color="subtle">
                {m.name} var här {ago(m.lastSeenAt, now)}
              </Text>
            ))}
          </Box>
        )}

        <Box flexDirection="row" alignItems="center" justifyContent="space-between" gap="sm">
          <Button key="summary" label="Sammanfatta inboxen" onPress={ask('Kolla min huddle-inbox och berätta vad som har hänt.')} />
          <Text size="caption" color="subtle" tabular>Uppdaterad {ago(new Date(v.updatedAt).toISOString(), now)}</Text>
        </Box>
      </Box>
    )
  })
}
