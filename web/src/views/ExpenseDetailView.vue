<script setup lang="ts">
// Single expense — read-only "detail" view, derived from the edit/new layout.
// STATIC scaffolding: the values below are placeholders, not real data.
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import StatusTag from '@/components/StatusTag.vue'

// Placeholder expense (NOT real data). Shape loosely follows the API's
// ExpenseResponse — money already formatted as pound strings for display.
const expense = {
  description: 'Team dinner — Bucharest trip',
  category: 'Accommodation and Meals',
  datedOn: '28 May 2026',
  currency: 'GBP',
  grossValue: '178.31',
  vatRate: 'Standard 20%',
  vatValue: '29.72',
  status: 'DRAFT',
  supplierName: 'Caru cu Bere',
  supplierVatNumber: 'RO 1234567',
  invoiceNumber: 'INV-2026-0512',
  receiptReference: 'R-0042',
  createdAt: '28 May 2026, 14:03',
  updatedAt: '28 May 2026, 14:03',
}

// label/value rows rendered read-only in the card.
const rows = [
  { label: 'Description', value: expense.description },
  { label: 'Category', value: expense.category },
  { label: 'Dated', value: expense.datedOn },
  { label: 'Currency', value: expense.currency },
  { label: 'Total value', value: `£${expense.grossValue}` },
  { label: 'VAT rate', value: expense.vatRate },
  { label: 'VAT amount', value: `£${expense.vatValue}` },
  { label: 'Supplier name', value: expense.supplierName },
  { label: 'Supplier VAT number', value: expense.supplierVatNumber },
  { label: 'Invoice number', value: expense.invoiceNumber },
  { label: 'Receipt reference', value: expense.receiptReference },
  { label: 'Created', value: expense.createdAt },
  { label: 'Last updated', value: expense.updatedAt },
]
</script>

<template>
  <AppLayout>
    <div class="page-head">
      <div class="page-head__title">
        <h1 class="page-title">Expense</h1>
        <StatusTag :status="expense.status" />
      </div>
      <div class="page-actions">
        <Button label="Edit" icon="pi pi-pencil" />
        <Button label="Back to list" severity="secondary" outlined />
      </div>
    </div>

    <FaCard title="Expense details">
      <dl class="detail">
        <div v-for="row in rows" :key="row.label" class="drow">
          <dt class="drow__label">{{ row.label }}</dt>
          <dd class="drow__value">{{ row.value }}</dd>
        </div>
      </dl>
    </FaCard>
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
.page-head__title {
  display: flex;
  align-items: center;
  gap: 12px;
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

.detail {
  margin: 0;
}
.drow {
  display: grid;
  grid-template-columns: 190px minmax(0, 1fr);
  gap: 16px;
  padding: 9px 0;
  border-bottom: 1px solid #eef1f4;
}
.drow:last-child {
  border-bottom: 0;
}
.drow__label {
  text-align: right;
  font-size: 14px;
  color: var(--fa-muted);
}
.drow__value {
  margin: 0;
  font-size: 14px;
  color: var(--fa-text);
}

@media (max-width: 640px) {
  .drow {
    grid-template-columns: 1fr;
    gap: 2px;
  }
  .drow__label {
    text-align: left;
  }
}
</style>
