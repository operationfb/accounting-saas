<script setup lang="ts">
// Expenses list — the "expense view". STATIC scaffolding: the rows, filters and
// buttons are placeholders with no behaviour. Styled after FreeAgent's
// out-of-pocket expenses table.
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
    <div class="page-head">
      <h1 class="page-title">Out-of-Pocket Expenses</h1>
      <div class="page-actions">
        <Button label="Import expenses" severity="secondary" outlined />
        <Button label="Add new" icon="pi pi-angle-down" icon-pos="right" />
      </div>
    </div>

    <div class="panel">
      <div class="panel__filters">
        <Select v-model="claimant" :options="claimantOptions" />
        <Select v-model="range" :options="rangeOptions" />
      </div>

      <table class="exp-table">
        <thead>
          <tr>
            <th>Date</th>
            <th>Description</th>
            <th>Status</th>
            <th class="num">Amount</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.id" class="exp-row">
            <td class="exp-date">
              <i class="pi pi-paperclip exp-clip" />
              <span>{{ row.date }}</span>
            </td>
            <td>
              <a href="#" class="fa-link exp-desc">{{ row.desc }}</a>
              <span class="exp-cat">{{ row.category }}</span>
            </td>
            <td><StatusTag :status="row.status" /></td>
            <td class="num">£{{ row.amount }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </AppLayout>
</template>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
  margin-bottom: 18px;
}
.page-title {
  margin: 0;
  font-size: 22px;
  font-weight: 700;
}
.page-actions {
  display: flex;
  gap: 10px;
}

.panel {
  background: #fff;
  border: 1px solid var(--fa-border);
  border-radius: 5px;
  overflow: hidden;
}
.panel__filters {
  display: flex;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--fa-border);
  flex-wrap: wrap;
}

.exp-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 14px;
}
.exp-table thead th {
  text-align: left;
  padding: 12px 16px;
  font-size: 13px;
  font-weight: 600;
  color: var(--fa-muted);
  border-bottom: 1px solid var(--fa-border);
}
.exp-table th.num,
.exp-table td.num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.exp-row td {
  padding: 14px 16px;
  border-bottom: 1px solid #eef1f4;
  vertical-align: middle;
}
.exp-row:hover {
  background: #f7fafc;
  cursor: pointer;
}
.exp-date {
  white-space: nowrap;
  color: var(--fa-text);
}
.exp-clip {
  color: var(--fa-muted);
  font-size: 13px;
  margin-right: 8px;
}
.exp-desc {
  font-weight: 600;
  margin-right: 8px;
}
.exp-cat {
  color: var(--fa-muted);
}
</style>
