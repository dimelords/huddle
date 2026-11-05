export type HuddleMember = {
  id: string
  name: string
  active: boolean
  lastSeenAt?: string
}

export type HuddleImage = {
  id: string
  name: string
  width: number
  height: number
}

export type HuddleQuestion = {
  id: number
  from: string
  to: string
  text: string
  status: string // pending | draft | approved | declined
  threadId: number
  createdAt?: string
  answer?: string // the approved answer
  declineReason?: string
  decidedAt?: string
  images: HuddleImage[] // sent with the question
  answerImages: HuddleImage[] // sent with the approved answer
}

export type HuddleView = {
  server: string // the MCP server that answered, '' until one did
  me: string
  meName: string
  group: string // the group the session is signed in to, or the only one
  groupName: string // its display name
  locked: boolean // the session is signed in to that group only
  groups: string[]
  members: HuddleMember[] // everyone else in the group(s)
  names: Record<string, string> // member id -> display name, me included
  awaiting: HuddleQuestion[] // addressed to me, not decided
  asked: HuddleQuestion[] // I asked, recent first
  toMe: HuddleQuestion[] // addressed to me, recent first
  error: string
  updatedAt: number // ms since epoch, 0 = never
}

declare module 'claude-code' {
  interface PluginState {
    huddle: {
      view: HuddleView
      seen: number[]
      expanded: string[] // texts shown whole: 'q<id>' for a question, 'a<id>' for its answer
      showAll: string[] // sections showing every item: 'to', 'from'
      working: number[] // questions handed to Claude to answer, until a draft appears
      reviewing: { id: number; at: number }[] // drafts sent to the approval form, and when
      thumbs: Record<string, string> // image id -> small JPEG, base64
    }
  }
}
