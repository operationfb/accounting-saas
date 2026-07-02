// Types for the Tala AI assistant chat endpoint (POST /api/v1/tala/chat).
// The conversation is stateless on the server — the SPA keeps the history and
// sends it each turn.

export interface TalaChatMessage {
  role: 'user' | 'assistant'
  content: string
}

// A guarded write Tala proposes. The SPA renders it as a card and, on Confirm,
// calls the existing domain endpoint (chosen by `kind`). Tala never mutates.
export interface TalaProposedAction {
  kind: string // 'create_expense' | 'approve_expense'
  title: string
  summary: string
  payload: unknown // shape depends on `kind` (see below)
}

export interface TalaChatResponse {
  reply: string
  proposed_actions: TalaProposedAction[]
  tool_calls: string[]
}

// Payload shapes by kind (what the backend puts in ProposedAction.payload).
export interface CreateExpenseProposal {
  description: string
  gross_value: string
  dated_on: string
  currency: string
  supplier_name: string
  category_hint: string
}

export interface ApproveExpenseProposal {
  expense_id: string
}
