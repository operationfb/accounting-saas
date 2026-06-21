<script setup lang="ts">
// Bank account form — DUAL-MODE:
//   /bank-accounts/new       → create (POST /api/v1/bank-accounts)
//   /bank-accounts/:id/edit  → edit   (PUT  /api/v1/bank-accounts/:id), pre-filled
// Models the FreeAgent "New Bank Account" screen. Managing accounts is owner/admin
// only — the backend enforces it; a non-admin sees the 403 in the form banner.
// Mirrors ContactEntryView (manual reactive validation) + the currency picker from
// ExpenseEntryView. Money is entered as text (never a float), validated to ≤2dp.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Checkbox from 'primevue/checkbox'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { getBankAccount, createBankAccount, updateBankAccount } from '@/services/bank-accounts.service'
import { listCurrencies } from '@/services/currencies.service'
import { buildCurrencyOptions, currencySymbolMap } from '@/lib/currency'
import type { Currency } from '@/types/currency'
import type { CreateBankAccountRequest } from '@/types/bank-account'
import type { ApiError } from '@/lib/api'

const router = useRouter()
const route = useRoute()

// Edit mode iff we're on /bank-accounts/:id/edit (the create route has no :id).
const editId = typeof route.params.id === 'string' ? route.params.id : undefined
const isEdit = !!editId

// Positive decimal with ≤2 dp — the backend's money parse is the final authority.
const MONEY_RE = /^\d+(\.\d{1,2})?$/

const statusOptions = [
  { label: 'Active', value: 'active' },
  { label: 'Closed', value: 'closed' },
]

// --- form state (seeded with the backend's defaults) ---
const defaults = () => ({
  name: '',
  currency: 'GBP',
  status: 'active',
  isPersonal: false,
  isPrimary: false,
  bankName: '',
  accountNumber: '',
  sortCode: '',
  routingNumber: '',
  iban: '',
  bic: '',
  showOnInvoices: true,
  openingBalance: '0.00',
  guessExplanations: true,
})
const form = reactive(defaults())

// --- reference data: currencies (the global ISO 4217 list) ---
const currencies = ref<Currency[]>([])
const currencyOptions = computed(() => buildCurrencyOptions(currencies.value))
// The amount-input symbol (£/€/$) is derived from the fetched list.
const currencySymbol = computed(() => currencySymbolMap(currencies.value)[form.currency] ?? '')
async function loadCurrencies() {
  try {
    currencies.value = await listCurrencies()
  } catch {
    // Non-fatal: leave the picker on its GBP default, which still submits.
  }
}

// --- edit-mode load state ---
const loadingAccount = ref(isEdit) // spinner until the record is ready
const loadError = ref('')
// Once an account has transactions, the opening balance is locked (it is the
// running-balance seed). The backend is the authority; this only disables the field.
const openingBalanceLocked = ref(false)
async function loadForEdit() {
  if (!editId) return
  loadingAccount.value = true
  loadError.value = ''
  try {
    const a = await getBankAccount(editId)
    form.name = a.name
    form.currency = a.currency || 'GBP'
    form.status = a.status || 'active'
    form.isPersonal = a.is_personal
    form.isPrimary = a.is_primary
    form.bankName = a.bank_name ?? ''
    form.accountNumber = a.account_number ?? ''
    form.sortCode = a.sort_code ?? ''
    form.routingNumber = a.routing_number ?? ''
    form.iban = a.iban ?? ''
    form.bic = a.bic ?? ''
    form.showOnInvoices = a.show_on_invoices
    form.openingBalance = a.opening_balance
    openingBalanceLocked.value = !a.opening_balance_editable
    form.guessExplanations = a.guess_explanations
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load this bank account.'
  } finally {
    loadingAccount.value = false
  }
}

onMounted(() => {
  loadCurrencies()
  if (isEdit) loadForEdit()
})

// --- validation ---
const errors = reactive<Record<string, string>>({})
function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (!form.name.trim()) {
    errors.name = 'Enter an account name.'
  }
  const bal = form.openingBalance.trim()
  if (bal !== '' && !MONEY_RE.test(bal)) {
    errors.openingBalance = 'Enter a valid amount with up to 2 decimal places.'
  }
  return Object.keys(errors).length === 0
}

// --- submit ---
const submitting = ref(false)
const formError = ref('')

function buildPayload(): CreateBankAccountRequest {
  const opt = (v: string) => {
    const t = v.trim()
    return t === '' ? undefined : t
  }
  return {
    name: form.name.trim(),
    currency: form.currency,
    status: form.status,
    is_personal: form.isPersonal,
    is_primary: form.isPrimary,
    bank_name: opt(form.bankName),
    account_number: opt(form.accountNumber),
    sort_code: opt(form.sortCode),
    routing_number: opt(form.routingNumber),
    iban: opt(form.iban),
    bic: opt(form.bic),
    show_on_invoices: form.showOnInvoices,
    opening_balance: form.openingBalance.trim() === '' ? '0.00' : form.openingBalance.trim(),
    guess_explanations: form.guessExplanations,
  }
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    if (editId) {
      await updateBankAccount(editId, buildPayload())
    } else {
      await createBankAccount(buildPayload())
    }
    router.push('/bank-accounts')
  } catch (err) {
    // 401 is already handled by apiFetch. 400/403/404/422 land here.
    formError.value =
      (err as ApiError)?.message ??
      (editId ? 'Could not save your changes. Please try again.' : 'Could not create the account. Please try again.')
  } finally {
    submitting.value = false
  }
}

function cancel() {
  router.push('/bank-accounts')
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">
      {{ isEdit ? 'Edit Bank Account' : 'New Bank Account' }}
    </h1>

    <!-- Edit: loading -->
    <FaCard v-if="isEdit && loadingAccount" title="Bank account">
      <div class="py-10 text-center text-fa-muted"><i class="pi pi-spin pi-spinner mr-2" />Loading…</div>
    </FaCard>

    <!-- Edit: load error -->
    <FaCard v-else-if="isEdit && loadError" title="Bank account">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button
          label="Back to bank accounts"
          severity="secondary"
          outlined
          @click="router.push('/bank-accounts')"
        />
      </div>
    </FaCard>

    <!-- The form (create, or edit once loaded) -->
    <template v-else>
      <div
        v-if="formError"
        class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
        role="alert"
      >
        {{ formError }}
      </div>

      <!-- Bank account -->
      <FaCard title="Bank account" note="Required fields *">
        <FormRow label="Account name" label-for="name" required>
          <InputText id="name" v-model="form.name" class="w-full sm:w-72" :invalid="!!errors.name" />
          <p v-if="errors.name" class="text-xs text-[#c0392b]">{{ errors.name }}</p>
        </FormRow>

        <FormRow label="Currency" label-for="currency">
          <Select
            id="currency"
            v-model="form.currency"
            :options="currencyOptions"
            option-label="label"
            option-value="value"
            option-disabled="disabled"
            class="w-full sm:w-72"
          />
        </FormRow>

        <FormRow label="Status" label-for="status">
          <Select
            id="status"
            v-model="form.status"
            :options="statusOptions"
            option-label="label"
            option-value="value"
            class="w-full sm:w-56"
          />
        </FormRow>

        <FormRow>
          <label class="inline-flex items-center gap-2 text-sm text-fa-text">
            <Checkbox v-model="form.isPersonal" binary input-id="personal" />
            <span>This is a personal account</span>
          </label>
        </FormRow>

        <FormRow>
          <label class="inline-flex items-center gap-2 text-sm text-fa-text">
            <Checkbox v-model="form.isPrimary" binary input-id="primary" />
            <span>Make this my primary account</span>
          </label>
        </FormRow>
      </FaCard>

      <!-- Optional details -->
      <FaCard title="Optional details">
        <FormRow label="Bank name" label-for="bank-name">
          <InputText id="bank-name" v-model="form.bankName" class="w-full sm:w-72" />
        </FormRow>

        <FormRow label="Account Number" label-for="account-number">
          <InputText id="account-number" v-model="form.accountNumber" class="w-full sm:w-56" />
        </FormRow>

        <FormRow label="Sort/Bank Code" label-for="sort-code">
          <InputText id="sort-code" v-model="form.sortCode" class="w-full sm:w-40" />
          <span class="block text-xs text-fa-muted">This is the UK 6-digit sort code.</span>
        </FormRow>

        <FormRow label="Routing Number" label-for="routing-number">
          <InputText id="routing-number" v-model="form.routingNumber" class="w-full sm:w-40" />
          <span class="block text-xs text-fa-muted">US accounts (ABA, 9 digits). Leave blank for UK.</span>
        </FormRow>

        <FormRow label="IBAN" label-for="iban">
          <InputText id="iban" v-model="form.iban" class="w-full sm:w-72" />
        </FormRow>

        <FormRow label="BIC / SWIFT" label-for="bic">
          <InputText id="bic" v-model="form.bic" class="w-full sm:w-40" />
        </FormRow>

        <FormRow>
          <label class="inline-flex items-center gap-2 text-sm text-fa-text">
            <Checkbox v-model="form.showOnInvoices" binary input-id="show-invoices" />
            <span>Show these details on Invoices</span>
          </label>
        </FormRow>
      </FaCard>

      <!-- Opening balance -->
      <FaCard title="Opening balance">
        <FormRow label="Balance" label-for="opening-balance" required>
          <InputGroup class="w-full sm:w-56">
            <InputGroupAddon>{{ currencySymbol || '£' }}</InputGroupAddon>
            <InputText
              id="opening-balance"
              v-model="form.openingBalance"
              placeholder="0.00"
              inputmode="decimal"
              :disabled="openingBalanceLocked"
              :invalid="!!errors.openingBalance"
            />
          </InputGroup>
          <p v-if="errors.openingBalance" class="text-xs text-[#c0392b]">{{ errors.openingBalance }}</p>
          <span v-if="openingBalanceLocked" class="block text-xs text-fa-muted">
            Locked — the opening balance can only be set before the account has any transactions.
          </span>
          <span v-else class="block text-xs text-fa-muted">
            The account balance at the start of your accounting start date. For accounts opened after
            this date, enter zero.
          </span>
        </FormRow>
      </FaCard>

      <!-- Guess explanations -->
      <FaCard title="Guess explanations">
        <FormRow>
          <label class="inline-flex items-center gap-2 text-sm text-fa-text">
            <Checkbox v-model="form.guessExplanations" binary input-id="guess" />
            <span>Guess explanations for my transactions</span>
          </label>
        </FormRow>
      </FaCard>

      <div class="flex items-center gap-3 py-2 pb-6">
        <Button
          :label="isEdit ? 'Save changes' : 'Create new account'"
          :loading="submitting"
          @click="submit"
        />
        <button type="button" class="font-semibold text-fa-blue hover:underline" @click="cancel">
          Cancel
        </button>
      </div>
    </template>
  </AppLayout>
</template>
