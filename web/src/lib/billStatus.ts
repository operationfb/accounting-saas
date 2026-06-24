// Presentation helper for a bill's DERIVED display_status — the coloured pill over
// the API's display_status. Mirrors lib/invoiceStatus.ts; reuses the same
// arbitrary-hex pill family as the invoice/expense/project status pills.
//
// The backend (internal/bills/service.go deriveDisplayStatus) emits: Unpaid (blue,
// a live payable) / Part paid (amber) / Paid (green) / Overdue (red) / Zero Value
// (grey). paid_value is written by the banking module; until then a bill stays
// Unpaid (or Overdue once past its due date).
const variants: Record<string, string> = {
  Unpaid: 'bg-[#e8f1fb] text-[#1f6fd0] border-[#cfe2f7]',
  'Part paid': 'bg-[#fdf6e3] text-[#8a6d3b] border-[#f0e0b6]',
  Paid: 'bg-[#eaf7e6] text-[#3f8038] border-[#cfe9c7]',
  Overdue: 'bg-[#fdecec] text-[#c0392b] border-[#f6d3d0]',
  'Zero Value': 'bg-[#eef1f4] text-[#5b6772] border-[#dde2e8]',
}

export function billStatusClass(displayStatus: string): string {
  return variants[displayStatus] ?? variants.Unpaid
}
