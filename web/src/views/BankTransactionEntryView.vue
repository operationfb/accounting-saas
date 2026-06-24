<script setup lang="ts">
// Manual bank transaction form — DUAL-MODE:
//   /bank-accounts/:id/transactions/new        → create (POST …/transactions)
//   /bank-accounts/:id/transactions/:txnId/edit → edit   (PUT  …/transactions/:txnId)
// A dedicated full-page view (like BankAccountEntryView), reached from the statement
// view's "More ▾ → Add transaction" and the per-row "Edit" on manual lines. Owner/admin
// only (the backend enforces it). On save/delete it navigates back to the statement,
// which refetches on mount — so balances + tabs refresh.
//
// Edit prefill reuses the statement endpoint (no GET-one API): load the account's
// transactions and find the row by id; only source='manual' lines are editable.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import InputText from 'primevue/inputtext'
import SelectButton from 'primevue/selectbutton'
import DatePicker from 'primevue/datepicker'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import {
  getBankAccountTransactions,
  createBankTransaction,
  updateBankTransaction,
  deleteBankTransaction,
} from '@/services/bank-accounts.service'
import { toISODate } from '@/lib/format'
import type { BankAccount, CreateBankTransactionRequest } from '@/types/bank-account'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const accountId = String(route.params.id)
const txnId = typeof route.params.txnId === 'string' ? route.params.txnId : undefined
const isEdit = !!txnId

// Positive decimal, ≤2 dp — the backend's money parse is the final authority.
const MONEY_RE = /^\d+(\.\d{1,2})?$/

const typeOptions = [
  { label: 'Money in', value: 'in' },
  { label: 'Money out', value: 'out' },
]

const form = reactive({
  datedOn: new Date() as Date | null,
  description: '',
  direction: 'out' as 'in' | 'out',
  amount: '',
  bankMemo: '',
})

// The £/$/€ addon for the amount input, from the account's currency.
const account = ref<BankAccount | null>(null)
const SYMBOLS: Record<string, string> = { GBP: '£', USD: '$', EUR: '€' }
const currencySymbol = computed(() =>
  account.value ? (SYMBOLS[account.value.currency] ?? account.value.currency) : '£',
)

// --- load (account for the currency; in edit mode also the row to prefill) ---
const loading = ref(true)
const loadError = ref('')
async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const { account: acc, transactions } = await getBankAccountTransactions(accountId)
    account.value = acc
    if (isEdit) {
      const t = transactions.find((x) => x.id === txnId)
      if (!t) {
        loadError.value = 'Transaction not found.'
        return
      }
      if (t.source !== 'manual') {
        loadError.value = 'Only manually-added transactions can be edited.'
        return
      }
      form.direction = t.money_in != null ? 'in' : 'out'
      form.amount = (t.money_in ?? t.money_out) ?? ''
      form.datedOn = new Date(`${t.dated_on}T00:00:00`)
      form.description = t.description ?? ''
      form.bankMemo = t.bank_memo ?? ''
    }
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load this transaction.'
  } finally {
    loading.value = false
  }
}
onMounted(load)

// --- validation ---
const errors = reactive<Record<string, string>>({})
function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (!form.datedOn) {
    errors.datedOn = 'Choose a date.'
  }
  const amt = form.amount.trim()
  if (!amt) {
    errors.amount = 'Enter an amount.'
  } else if (!MONEY_RE.test(amt)) {
    errors.amount = 'Enter a valid amount with up to 2 decimal places.'
  }
  return Object.keys(errors).length === 0
}

// --- submit / delete ---
const submitting = ref(false)
const formError = ref('')

function buildPayload(): CreateBankTransactionRequest {
  const opt = (v: string) => {
    const t = v.trim()
    return t === '' ? undefined : t
  }
  return {
    dated_on: toISODate(form.datedOn as Date),
    description: opt(form.description),
    direction: form.direction,
    amount: form.amount.trim(),
    bank_memo: opt(form.bankMemo),
  }
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    if (txnId) {
      await updateBankTransaction(accountId, txnId, buildPayload())
    } else {
      await createBankTransaction(accountId, buildPayload())
    }
    router.push(`/bank-accounts/${accountId}`)
  } catch (err) {
    formError.value =
      (err as ApiError)?.message ??
      (txnId ? 'Could not save the transaction. Please try again.' : 'Could not add the transaction. Please try again.')
  } finally {
    submitting.value = false
  }
}

async function removeTxn() {
  if (!txnId || submitting.value) return
  if (!window.confirm('Delete this transaction? This cannot be undone.')) return
  submitting.value = true
  formError.value = ''
  try {
    await deleteBankTransaction(accountId, txnId)
    router.push(`/bank-accounts/${accountId}`)
  } catch (err) {
    formError.value = (err as ApiError)?.message ?? 'Could not delete the transaction.'
  } finally {
    submitting.value = false
  }
}

function cancel() {
  router.push(`/bank-accounts/${accountId}`)
}
</script>

<template>
  <AppLayout>
    <h1 class="text-[22px] font-bold">{{ isEdit ? 'Edit Transaction' : 'New Transaction' }}</h1>
    <p v-if="account" class="mb-[18px] text-sm text-fa-muted">{{ account.name }}</p>
    <div v-else class="mb-[18px]" />

    <!-- Edit: loading -->
    <FaCard v-if="isEdit && loading" title="Transaction">
      <div class="py-10 text-center text-fa-muted"><i class="pi pi-spin pi-spinner mr-2" />Loading…</div>
    </FaCard>

    <!-- Edit: load error (not found / not editable) -->
    <FaCard v-else-if="isEdit && loadError" title="Transaction">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button label="Back to transactions" severity="secondary" outlined @click="cancel" />
      </div>
    </FaCard>

    <!-- The form -->
    <template v-else>
      <div
        v-if="formError"
        class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
        role="alert"
      >
        {{ formError }}
      </div>

      <FaCard title="Transaction" note="Required fields *">
        <FormRow label="Type" label-for="direction">
          <SelectButton
            v-model="form.direction"
            :options="typeOptions"
            option-label="label"
            option-value="value"
            :allow-empty="false"
            aria-labelledby="direction"
          />
        </FormRow>

        <FormRow label="Date" label-for="dated" required>
          <DatePicker
            id="dated"
            v-model="form.datedOn"
            date-format="dd M yy"
            show-icon
            :show-on-focus="false"
            :invalid="!!errors.datedOn"
            class="w-full sm:w-72"
          />
          <p v-if="errors.datedOn" class="text-xs text-[#c0392b]">{{ errors.datedOn }}</p>
        </FormRow>

        <FormRow label="Description" label-for="description">
          <InputText id="description" v-model="form.description" class="w-full sm:w-96" />
        </FormRow>

        <FormRow label="Amount" label-for="amount" required>
          <InputGroup class="w-full sm:w-56">
            <InputGroupAddon>{{ currencySymbol }}</InputGroupAddon>
            <InputText
              id="amount"
              v-model="form.amount"
              placeholder="0.00"
              inputmode="decimal"
              :invalid="!!errors.amount"
            />
          </InputGroup>
          <p v-if="errors.amount" class="text-xs text-[#c0392b]">{{ errors.amount }}</p>
        </FormRow>

        <FormRow label="Bank memo" label-for="bank-memo">
          <InputText id="bank-memo" v-model="form.bankMemo" class="w-full sm:w-96" />
          <span class="block text-xs text-fa-muted">Optional raw bank narrative.</span>
        </FormRow>
      </FaCard>

      <div class="flex items-center gap-3 py-2 pb-6">
        <Button
          :label="isEdit ? 'Save changes' : 'Add transaction'"
          :loading="submitting"
          @click="submit"
        />
        <button type="button" class="font-semibold text-fa-green hover:underline" @click="cancel">
          Cancel
        </button>
        <button
          v-if="isEdit"
          type="button"
          class="ml-auto font-semibold text-[#c0392b] hover:underline"
          :disabled="submitting"
          @click="removeTxn"
        >
          Delete transaction
        </button>
      </div>
    </template>
  </AppLayout>
</template>
