<script setup lang="ts">
// A single guarded-write proposal from Tala, rendered as a confirmation card.
// The agent loop never mutates — CONFIRMING here calls the existing domain
// endpoint (createExpense / changeExpenseStatus), which does the real,
// already-authorised work. Dismiss removes the card.
import { ref, onMounted, computed } from 'vue'
import type {
  TalaProposedAction,
  CreateExpenseProposal,
  ApproveExpenseProposal,
} from '@/types/tala'
import type { ExpenseCategory, CreateExpenseRequest } from '@/types/expense'
import { listCategories, createExpense, changeExpenseStatus } from '@/services/expenses.service'
import type { ApiError } from '@/lib/api'

const props = defineProps<{ proposal: TalaProposedAction }>()
const emit = defineEmits<{ (e: 'dismissed'): void }>()

type Status = 'idle' | 'submitting' | 'done' | 'error'
const status = ref<Status>('idle')
const errorMsg = ref('')

// create_expense: the user picks the final category on the card (the backend
// endpoint requires a category id; Tala only passes a name hint).
const categories = ref<ExpenseCategory[]>([])
const selectedCategoryId = ref('')

const createPayload = computed(() =>
  props.proposal.kind === 'create_expense' ? (props.proposal.payload as CreateExpenseProposal) : null,
)
const approvePayload = computed(() =>
  props.proposal.kind === 'approve_expense' ? (props.proposal.payload as ApproveExpenseProposal) : null,
)

onMounted(async () => {
  if (props.proposal.kind !== 'create_expense') return
  try {
    categories.value = await listCategories()
    // Pre-select the category whose name best matches Tala's hint; else the first.
    const hint = (createPayload.value?.category_hint ?? '').toLowerCase().trim()
    const match = hint ? categories.value.find((c) => c.name.toLowerCase().includes(hint)) : undefined
    selectedCategoryId.value = match?.id ?? categories.value[0]?.id ?? ''
  } catch {
    /* leave the dropdown empty — the user can still pick once it loads */
  }
})

async function confirm() {
  status.value = 'submitting'
  errorMsg.value = ''
  try {
    if (createPayload.value) {
      if (!selectedCategoryId.value) {
        status.value = 'error'
        errorMsg.value = 'Please choose a category first.'
        return
      }
      const p = createPayload.value
      const req: CreateExpenseRequest = {
        category_id: selectedCategoryId.value,
        dated_on: p.dated_on,
        description: p.description,
        gross_value: p.gross_value,
        currency: p.currency || 'GBP',
      }
      if (p.supplier_name) req.supplier_name = p.supplier_name
      await createExpense(req)
    } else if (approvePayload.value) {
      await changeExpenseStatus(approvePayload.value.expense_id, 'approve')
    } else {
      status.value = 'error'
      errorMsg.value = 'This action type is not supported yet.'
      return
    }
    status.value = 'done'
  } catch (e) {
    status.value = 'error'
    errorMsg.value = (e as ApiError)?.message ?? 'Something went wrong.'
  }
}
</script>

<template>
  <div class="mt-2 rounded-lg border border-[#2d6a4f]/30 bg-[#f6faf6] p-3 text-sm">
    <div class="mb-1 flex items-center gap-2">
      <span
        class="rounded bg-[#2d6a4f] px-1.5 py-0.5 text-[11px] font-semibold uppercase tracking-wide text-white"
        >Action</span
      >
      <strong class="text-gray-800">{{ proposal.title }}</strong>
    </div>
    <p class="mb-2 text-gray-600">{{ proposal.summary }}</p>

    <div v-if="createPayload" class="mb-2 space-y-1">
      <div class="flex justify-between">
        <span class="text-gray-500">Amount</span>
        <b>{{ createPayload.currency }} {{ createPayload.gross_value }}</b>
      </div>
      <div class="flex justify-between">
        <span class="text-gray-500">Date</span><b>{{ createPayload.dated_on }}</b>
      </div>
      <div v-if="createPayload.supplier_name" class="flex justify-between">
        <span class="text-gray-500">Supplier</span><b>{{ createPayload.supplier_name }}</b>
      </div>
      <label class="flex items-center justify-between gap-2">
        <span class="text-gray-500">Category</span>
        <select
          v-model="selectedCategoryId"
          :disabled="status === 'done'"
          class="min-w-0 flex-1 rounded border border-gray-300 px-2 py-1 text-sm"
        >
          <option v-for="c in categories" :key="c.id" :value="c.id">
            {{ c.name }} ({{ c.nominal_code }})
          </option>
        </select>
      </label>
    </div>

    <p v-if="status === 'done'" class="font-medium text-[#2d6a4f]">✓ Done</p>
    <p v-else-if="status === 'error'" class="text-red-600">{{ errorMsg }}</p>

    <div v-if="status !== 'done'" class="mt-2 flex gap-2">
      <button
        class="rounded bg-[#2d6a4f] px-3 py-1.5 text-sm font-medium text-white hover:bg-[#245A42] disabled:opacity-50"
        :disabled="status === 'submitting'"
        @click="confirm"
      >
        {{ status === 'submitting' ? 'Working…' : 'Confirm' }}
      </button>
      <button
        class="rounded px-3 py-1.5 text-sm font-medium text-gray-600 hover:bg-gray-100 disabled:opacity-50"
        :disabled="status === 'submitting'"
        @click="emit('dismissed')"
      >
        Dismiss
      </button>
    </div>
  </div>
</template>
