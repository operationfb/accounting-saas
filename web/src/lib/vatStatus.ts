// Presentation helper for a VAT return period's DERIVED display_status — the
// coloured pill over the API's display_status. Mirrors lib/billStatus.ts and reuses
// the same arbitrary-hex pill family as the other status pills.
//
// v1 emits: Open (the period is still in progress — blue) and Unfiled (the period
// has ended and no return has been filed yet — amber). Later slices add Filed /
// Marked as filed (green) and Overdue (red) once returns are saved/submitted.
const variants: Record<string, string> = {
  Open: 'bg-[#e8f1fb] text-[#1f6fd0] border-[#cfe2f7]',
  Unfiled: 'bg-[#fdf6e3] text-[#8a6d3b] border-[#f0e0b6]',
  Filed: 'bg-[#eaf7e6] text-[#3f8038] border-[#cfe9c7]',
  'Marked as filed': 'bg-[#eaf7e6] text-[#3f8038] border-[#cfe9c7]',
  Overdue: 'bg-[#fdecec] text-[#c0392b] border-[#f6d3d0]',
}

export function vatStatusClass(displayStatus: string): string {
  return variants[displayStatus] ?? variants.Unfiled
}
