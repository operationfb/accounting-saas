<script setup lang="ts">
// Expenses list — the "expense view". Wired to GET /api/v1/expenses.
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Select from 'primevue/select'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import StatusTag from '@/components/StatusTag.vue'
import { listExpenses } from '@/services/expenses.service'
import { formatMoney, formatDate } from '@/lib/format'
import type { Expense } from '@/types/expense'
import type { ApiError } from '@/lib/api'

const router = useRouter()

// Filter UI — rendered but not wired to any filtering yet (static placeholders).
const claimant = ref('All claimants')
const claimantOptions = ['All claimants']
const range = ref('Most recent')
const rangeOptions = ['Most recent', 'This month', 'This quarter', 'All']

const headers = [
  { label: 'Date', num: false },
  { label: 'Description', num: false },
  { label: 'Status', num: false },
  { label: 'Amount', num: true },
]

const expenses = ref<Expense[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    expenses.value = await listExpenses()
  } catch (err) {
    // A 401 is already handled by apiFetch (logout + redirect); any other
    // failure shows here with a retry.
    error.value = (err as ApiError)?.message ?? 'Could not load expenses.'
  } finally {
    loading.value = false
  }
}

onMounted(load)

function openExpense(id: string) {
  router.push(`/expenses/${id}`)
}

function newExpense() {
  router.push('/expenses/new')
}
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Out-of-Pocket Expenses</h1>
      <div class="flex gap-2.5">
        <Button label="Import expenses" severity="secondary" outlined />
        <Button label="Add new" icon="pi pi-angle-down" icon-pos="right" @click="newExpense" />
      </div>
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <div class="flex flex-wrap gap-3 border-b border-fa-border px-4 py-3.5">
        <Select v-model="claimant" :options="claimantOptions" />
        <Select v-model="range" :options="rangeOptions" />
      </div>

      <!-- Loading -->
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading expenses…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Empty -->
      <div v-else-if="expenses.length === 0" class="px-4 py-14 text-center">
        <p class="mb-1 font-semibold">No expenses yet</p>
        <p class="mb-4 text-sm text-fa-muted">Add your first out-of-pocket expense to see it here.</p>
        <Button label="New expense" @click="newExpense" />
      </div>

      <!-- Data -->
      <table v-else class="w-full border-collapse text-sm">
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
          <tr
            v-for="exp in expenses"
            :key="exp.id"
            class="cursor-pointer hover:bg-[#f7fafc]"
            @click="openExpense(exp.id)"
          >
            <td class="whitespace-nowrap border-b border-[#eef1f4] px-4 py-3.5 align-middle">
              {{ formatDate(exp.dated_on) }}
            </td>
            <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
              <RouterLink
                :to="{ name: 'expense-detail', params: { id: exp.id } }"
                class="font-semibold text-fa-blue hover:underline"
                @click.stop
                >{{ exp.description }}</RouterLink
              >
              <span v-if="exp.supplier_name" class="ml-2 text-fa-muted">{{ exp.supplier_name }}</span>
            </td>
            <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
              <StatusTag :status="exp.status" />
            </td>
            <td class="border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle tabular-nums">
              {{ formatMoney(exp.gross_value, exp.currency) }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </AppLayout>
</template>
