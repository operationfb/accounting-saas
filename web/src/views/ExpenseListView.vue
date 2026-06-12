<script setup lang="ts">
// Expenses list — the "expense view". STATIC scaffolding: rows, filters and
// buttons are placeholders with no behaviour. Styled after FA's out-of-pocket
// expenses table.
import { ref } from 'vue'
import Select from 'primevue/select'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import StatusTag from '@/components/StatusTag.vue'

// Placeholder filter state (renders the dropdowns; no filtering logic).
const claimant = ref('Sinem Soydar Gunal')
const claimantOptions = ['Sinem Soydar Gunal']
const range = ref('10 Most Recent')
const rangeOptions = ['10 Most Recent', 'This month', 'This quarter', 'All']

const headers = [
  { label: 'Date', num: false },
  { label: 'Description', num: false },
  { label: 'Status', num: false },
  { label: 'Amount', num: true },
]

// Placeholder rows — NOT real data.
const rows = [
  { id: '1', date: '28 Apr 26', desc: 'Meal', category: 'Accommodation and Meals', amount: '44.00', status: 'PAID' },
  { id: '2', date: '30 Apr 26', desc: 'Food', category: 'Accommodation and Meals', amount: '60.00', status: 'PAID' },
  { id: '3', date: '15 May 26', desc: 'Travel — Bucharest', category: 'Travel', amount: '2,128.75', status: 'APPROVED' },
  { id: '4', date: '16 May 26', desc: 'Travel — Bucharest', category: 'Travel', amount: '851.20', status: 'APPROVED' },
  { id: '5', date: '18 May 26', desc: 'Travel — Bucharest', category: 'Travel', amount: '121.15', status: 'SUBMITTED' },
  { id: '6', date: '19 May 26', desc: 'Meal', category: 'Accommodation and Meals', amount: '364.00', status: 'SUBMITTED' },
  { id: '7', date: '26 May 26', desc: 'Food', category: 'Accommodation and Meals', amount: '181.40', status: 'DRAFT' },
  { id: '8', date: '28 May 26', desc: 'Food', category: 'Accommodation and Meals', amount: '178.31', status: 'DRAFT' },
]
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Out-of-Pocket Expenses</h1>
      <div class="flex gap-2.5">
        <Button label="Import expenses" severity="secondary" outlined />
        <Button label="Add new" icon="pi pi-angle-down" icon-pos="right" />
      </div>
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <div class="flex flex-wrap gap-3 border-b border-fa-border px-4 py-3.5">
        <Select v-model="claimant" :options="claimantOptions" />
        <Select v-model="range" :options="rangeOptions" />
      </div>

      <table class="w-full border-collapse text-sm">
        <thead>
          <tr>
            <th
              v-for="h in headers"
              :key="h.label"
              class="border-b border-fa-border px-4 py-3 text-[13px] font-semibold text-fa-muted"
              :class="h.num ? 'text-right' : 'text-left'"
            >
              {{ h.label }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.id" class="cursor-pointer hover:bg-[#f7fafc]">
            <td class="whitespace-nowrap border-b border-[#eef1f4] px-4 py-3.5 align-middle">
              <i class="pi pi-paperclip mr-2 text-[13px] text-fa-muted" />
              <span>{{ row.date }}</span>
            </td>
            <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
              <a href="#" class="mr-2 font-semibold text-fa-blue hover:underline">{{ row.desc }}</a>
              <span class="text-fa-muted">{{ row.category }}</span>
            </td>
            <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
              <StatusTag :status="row.status" />
            </td>
            <td class="border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle tabular-nums">
              £{{ row.amount }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </AppLayout>
</template>
