// Presentation helpers for an invoice's status — the coloured pill (over the API's
// display_status) and which step of the Draft → Sent → Paid tracker is active.
// Kept here rather than in StatusTag.vue, which is keyed to the EXPENSE status
// codes; invoices show the human-readable display_status instead.

// Pill colours per display_status. Reuses the same arbitrary-hex family as the
// project/expense status pills.
const variants: Record<string, string> = {
  Draft: 'bg-[#eef1f4] text-[#5b6772] border-[#dde2e8]',
  Scheduled: 'bg-[#fdf6e3] text-[#8a6d3b] border-[#f0e0b6]',
  Open: 'bg-[#e8f1fb] text-[#1f6fd0] border-[#cfe2f7]',
  'Zero Value': 'bg-[#eef1f4] text-[#5b6772] border-[#dde2e8]',
  Overdue: 'bg-[#fdecec] text-[#c0392b] border-[#f6d3d0]',
  Paid: 'bg-[#eaf7e6] text-[#3f8038] border-[#cfe9c7]',
  Overpaid: 'bg-[#eaf7e6] text-[#3f8038] border-[#cfe9c7]',
  'Written off': 'bg-[#fdecec] text-[#c0392b] border-[#f6d3d0]',
  Refunded: 'bg-[#e6f5f3] text-[#167d6e] border-[#c8e9e4]',
}

export function invoiceStatusClass(displayStatus: string): string {
  return variants[displayStatus] ?? variants.Draft
}

// Which node of the Draft → Sent → Paid tracker the invoice sits at, from the
// stored status + derived display_status. WRITTEN_OFF / REFUNDED are off the happy
// path → 'other' (the tracker isn't shown for those).
export type InvoiceStep = 'draft' | 'sent' | 'paid' | 'other'

export function invoiceStep(status: string, displayStatus: string): InvoiceStep {
  if (status === 'WRITTEN_OFF' || status === 'REFUNDED') return 'other'
  if (displayStatus === 'Paid' || displayStatus === 'Overpaid') return 'paid'
  if (status === 'SENT') return 'sent'
  return 'draft' // DRAFT or SCHEDULED
}
