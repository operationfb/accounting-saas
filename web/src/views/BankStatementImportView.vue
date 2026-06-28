<script setup lang="ts">
// Statement import — a dedicated full-page view. Reached from the statement view's
// "Upload statement" button (owner/admin only — the backend enforces it).
//
// Flow is detect → confirm → commit:
//   1. pick a file → POST …/import/preview (no import) → the backend AUTO-DETECTS the
//      column mapping + shows how the first rows would be read.
//   2. confirm/adjust the column→field dropdowns + date format; the preview re-reads live.
//   3. Import → POST …/import with the agreed mapping → the imported/skipped summary.
// An OFX file is self-describing, so step 2 shows the preview rows with no dropdowns.
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import { getBankAccount, previewBankStatement, importBankStatement } from '@/services/bank-accounts.service'
import type { BankAccount, ColumnMapping, PreviewRow, StatementImportPreview, StatementImportResult } from '@/types/bank-account'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const accountId = String(route.params.id)

const templateHref = `${import.meta.env.BASE_URL}statement_import_template.csv`
const MAX_BYTES = 5 * 1024 * 1024 // mirrors the backend's maxStatementUploadBytes (5 MiB)

// --- account (for the header name) ---
const account = ref<BankAccount | null>(null)
const loading = ref(true)
const loadError = ref('')
onMounted(async () => {
  try {
    account.value = await getBankAccount(accountId)
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load this bank account.'
  } finally {
    loading.value = false
  }
})

// --- the import flow (file → preview/mapping → result) ---
const fileInput = ref<HTMLInputElement | null>(null)
const file = ref<File | null>(null)
const previewing = ref(false)
const preview = ref<StatementImportPreview | null>(null) // holds columns + date-format options + format
const mapping = ref<ColumnMapping | null>(null) // editable; null for OFX
const previewRows = ref<PreviewRow[]>([])
const warnings = ref<string[]>([])
const totalRows = ref(0)
const importing = ref(false)
const formError = ref('')
const result = ref<StatementImportResult | null>(null)

const isOFX = computed(() => preview.value?.format === 'ofx')
const columns = computed(() => preview.value?.columns ?? [])
const dateFormatOptions = computed(() => preview.value?.date_format_options ?? [])
const showBalanceCol = computed(() => previewRows.value.some((r) => !!r.balance))

// canImport gates the Import button: every required field of the chosen amount shape must
// be assigned (OFX needs no mapping). The backend re-validates on commit (422) regardless.
const canImport = computed(() => {
  if (!preview.value) return false
  if (isOFX.value) return true
  const m = mapping.value
  if (!m || m.date_column == null || m.description_column == null || !m.date_format) return false
  if (m.amount_format === 'signed') return m.amount_column != null
  if (m.amount_format === 'split') return m.money_in_column != null && m.money_out_column != null
  return false
})

// normaliseMapping forces explicit nulls for the optional columns so the <select> v-models
// match their "— None —" option (an undefined wouldn't), and pins the amount_format union.
function normaliseMapping(m: NonNullable<StatementImportPreview['mapping']>): ColumnMapping {
  return {
    date_column: m.date_column ?? null,
    description_column: m.description_column ?? null,
    amount_format: (m.amount_format || '') as ColumnMapping['amount_format'],
    amount_column: m.amount_column ?? null,
    money_in_column: m.money_in_column ?? null,
    money_out_column: m.money_out_column ?? null,
    balance_column: m.balance_column ?? null,
    memo_column: m.memo_column ?? null,
    date_format: m.date_format,
  }
}

function onFilePicked(e: Event) {
  formError.value = ''
  const picked = (e.target as HTMLInputElement).files?.[0] ?? null
  ;(e.target as HTMLInputElement).value = '' // allow re-picking the same file
  if (!picked) return
  if (picked.size > MAX_BYTES) {
    formError.value = 'That file is too large (max 5 MB).'
    return
  }
  file.value = picked
  void doPreview()
}

// doPreview is the DETECT step: upload the file, get the proposed mapping + sample rows.
async function doPreview() {
  if (!file.value) return
  previewing.value = true
  formError.value = ''
  try {
    const resp = await previewBankStatement(accountId, file.value)
    preview.value = resp
    mapping.value = resp.mapping ? normaliseMapping(resp.mapping) : null
    previewRows.value = resp.preview_rows ?? []
    warnings.value = resp.warnings ?? []
    totalRows.value = resp.total_rows
  } catch (err) {
    formError.value = (err as ApiError)?.message ?? 'Could not read that file. Please check it and try again.'
    resetFlow()
  } finally {
    previewing.value = false
  }
}

// refreshPreview re-reads the rows through the user's edited mapping, for live feedback.
async function refreshPreview() {
  if (!file.value || !mapping.value || isOFX.value) return
  try {
    const resp = await previewBankStatement(accountId, file.value, mapping.value)
    previewRows.value = resp.preview_rows ?? []
    warnings.value = resp.warnings ?? []
  } catch (err) {
    formError.value = (err as ApiError)?.message ?? 'Could not preview with that mapping.'
  }
}

// doImport is the COMMIT step: import with the confirmed mapping (none for OFX).
async function doImport() {
  if (!file.value || !canImport.value || importing.value) return
  importing.value = true
  formError.value = ''
  try {
    result.value = await importBankStatement(accountId, file.value, isOFX.value ? undefined : mapping.value!)
  } catch (err) {
    formError.value = (err as ApiError)?.message ?? 'Could not import the statement. Please check the mapping and try again.'
  } finally {
    importing.value = false
  }
}

function resetFlow() {
  preview.value = null
  mapping.value = null
  previewRows.value = []
  warnings.value = []
  totalRows.value = 0
  file.value = null
}
function chooseDifferentFile() {
  formError.value = ''
  resetFlow()
}
function importAnother() {
  result.value = null
  formError.value = ''
  resetFlow()
}
function backToStatement() {
  router.push(`/bank-accounts/${accountId}`)
}
</script>

<template>
  <AppLayout>
    <h1 class="text-[22px] font-bold">Import statement</h1>
    <p v-if="account" class="mb-[18px] text-sm text-fa-muted">{{ account.name }}</p>
    <div v-else class="mb-[18px]" />

    <!-- Account load states -->
    <FaCard v-if="loading" title="Upload a statement">
      <div class="py-10 text-center text-fa-muted"><i class="pi pi-spin pi-spinner mr-2" />Loading…</div>
    </FaCard>
    <FaCard v-else-if="loadError" title="Upload a statement">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button label="Back to bank accounts" severity="secondary" outlined @click="router.push('/bank-accounts')" />
      </div>
    </FaCard>

    <!-- Result -->
    <FaCard v-else-if="result" title="Import complete">
      <p class="text-sm">
        <strong>{{ result.imported }}</strong> imported<span v-if="result.skipped_duplicates > 0">
          · <strong>{{ result.skipped_duplicates }}</strong> skipped (already imported)</span>
        · {{ result.total }} row{{ result.total === 1 ? '' : 's' }} in the file.
      </p>
      <div class="mt-4 flex items-center gap-3">
        <Button label="View statement" @click="backToStatement" />
        <button type="button" class="font-semibold text-fa-blue hover:underline" @click="importAnother">
          Import another file
        </button>
      </div>
    </FaCard>

    <!-- Step 2: confirm the detected mapping (CSV) or just review (OFX) -->
    <FaCard v-else-if="preview" title="Check the columns">
      <p v-if="isOFX" class="mb-4 text-sm text-fa-muted">
        We detected an <strong>OFX</strong> file — it describes its own columns, so there's nothing to map.
        Check the preview below and import.
      </p>
      <p v-else class="mb-4 text-sm text-fa-muted">
        We detected your file's layout. Check each column is mapped to the right field, set the date format,
        then import. We'll skip any rows already imported.
      </p>

      <!-- Warnings from detection -->
      <ul v-if="warnings.length" class="mb-4 space-y-1">
        <li
          v-for="(w, i) in warnings"
          :key="i"
          class="rounded border border-[#f4e3c0] bg-[#fdf6e7] px-3 py-2 text-[13px] text-[#8a6d3b]"
        >
          {{ w }}
        </li>
      </ul>

      <!-- Mapping form (CSV only) -->
      <div v-if="!isOFX && mapping" class="mb-5 grid gap-4 sm:grid-cols-2">
        <!-- Amount shape -->
        <div class="sm:col-span-2">
          <span class="mb-1 block text-[13px] font-semibold">How is the amount shown?</span>
          <div class="flex flex-col gap-1 text-sm sm:flex-row sm:gap-5">
            <label class="inline-flex items-center gap-2">
              <input type="radio" value="signed" v-model="mapping.amount_format" @change="refreshPreview" />
              One amount column (a leading − means money out)
            </label>
            <label class="inline-flex items-center gap-2">
              <input type="radio" value="split" v-model="mapping.amount_format" @change="refreshPreview" />
              Separate money-in / money-out columns
            </label>
          </div>
        </div>

        <label class="block">
          <span class="mb-1 block text-[13px] font-semibold">Date column</span>
          <select
            v-model="mapping.date_column"
            @change="refreshPreview"
            class="w-full rounded-[5px] border border-fa-border bg-white px-3 py-2 text-sm"
          >
            <option :value="null" disabled>Select a column…</option>
            <option v-for="c in columns" :key="c.index" :value="c.index">{{ c.header }}</option>
          </select>
        </label>

        <label class="block">
          <span class="mb-1 block text-[13px] font-semibold">Description column</span>
          <select
            v-model="mapping.description_column"
            @change="refreshPreview"
            class="w-full rounded-[5px] border border-fa-border bg-white px-3 py-2 text-sm"
          >
            <option :value="null" disabled>Select a column…</option>
            <option v-for="c in columns" :key="c.index" :value="c.index">{{ c.header }}</option>
          </select>
        </label>

        <!-- Signed: one amount column -->
        <label v-if="mapping.amount_format === 'signed'" class="block">
          <span class="mb-1 block text-[13px] font-semibold">Amount column</span>
          <select
            v-model="mapping.amount_column"
            @change="refreshPreview"
            class="w-full rounded-[5px] border border-fa-border bg-white px-3 py-2 text-sm"
          >
            <option :value="null" disabled>Select a column…</option>
            <option v-for="c in columns" :key="c.index" :value="c.index">{{ c.header }}</option>
          </select>
        </label>

        <!-- Split: money-in + money-out -->
        <template v-if="mapping.amount_format === 'split'">
          <label class="block">
            <span class="mb-1 block text-[13px] font-semibold">Money-in column</span>
            <select
              v-model="mapping.money_in_column"
              @change="refreshPreview"
              class="w-full rounded-[5px] border border-fa-border bg-white px-3 py-2 text-sm"
            >
              <option :value="null" disabled>Select a column…</option>
              <option v-for="c in columns" :key="c.index" :value="c.index">{{ c.header }}</option>
            </select>
          </label>
          <label class="block">
            <span class="mb-1 block text-[13px] font-semibold">Money-out column</span>
            <select
              v-model="mapping.money_out_column"
              @change="refreshPreview"
              class="w-full rounded-[5px] border border-fa-border bg-white px-3 py-2 text-sm"
            >
              <option :value="null" disabled>Select a column…</option>
              <option v-for="c in columns" :key="c.index" :value="c.index">{{ c.header }}</option>
            </select>
          </label>
        </template>

        <label class="block">
          <span class="mb-1 block text-[13px] font-semibold">Date format</span>
          <select
            v-model="mapping.date_format"
            @change="refreshPreview"
            class="w-full rounded-[5px] border border-fa-border bg-white px-3 py-2 text-sm"
          >
            <option v-for="o in dateFormatOptions" :key="o.layout" :value="o.layout">{{ o.label }}</option>
          </select>
        </label>

        <label class="block">
          <span class="mb-1 block text-[13px] font-semibold">
            Balance column <span class="font-normal text-fa-muted">(optional)</span>
          </span>
          <select
            v-model="mapping.balance_column"
            @change="refreshPreview"
            class="w-full rounded-[5px] border border-fa-border bg-white px-3 py-2 text-sm"
          >
            <option :value="null">— None —</option>
            <option v-for="c in columns" :key="c.index" :value="c.index">{{ c.header }}</option>
          </select>
        </label>

        <label class="block">
          <span class="mb-1 block text-[13px] font-semibold">
            Bank memo column <span class="font-normal text-fa-muted">(optional)</span>
          </span>
          <select
            v-model="mapping.memo_column"
            @change="refreshPreview"
            class="w-full rounded-[5px] border border-fa-border bg-white px-3 py-2 text-sm"
          >
            <option :value="null">— None —</option>
            <option v-for="c in columns" :key="c.index" :value="c.index">{{ c.header }}</option>
          </select>
        </label>
      </div>

      <!-- Live preview of the first rows -->
      <h3 class="mb-2 text-sm font-bold">
        Preview <span class="font-normal text-fa-muted">(first {{ previewRows.length }} of {{ totalRows }} rows)</span>
      </h3>
      <div class="mb-5 overflow-x-auto rounded-[5px] border border-fa-border">
        <table class="w-full border-collapse text-[13px]">
          <thead>
            <tr class="bg-[#f7fafc] text-left text-fa-muted">
              <th class="border-b border-fa-border px-3 py-2 font-semibold">Date</th>
              <th class="border-b border-fa-border px-3 py-2 font-semibold">Description</th>
              <th class="border-b border-fa-border px-3 py-2 text-right font-semibold">Money in</th>
              <th class="border-b border-fa-border px-3 py-2 text-right font-semibold">Money out</th>
              <th v-if="showBalanceCol" class="border-b border-fa-border px-3 py-2 text-right font-semibold">Balance</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(r, i) in previewRows" :key="i" class="align-top">
              <td
                v-if="r.error"
                :colspan="showBalanceCol ? 5 : 4"
                class="border-b border-[#eef1f4] px-3 py-2 text-[#c0392b]"
              >
                <i class="pi pi-exclamation-triangle mr-1" />{{ r.error }}<span v-if="r.description"> — {{ r.description }}</span>
              </td>
              <template v-else>
                <td class="whitespace-nowrap border-b border-[#eef1f4] px-3 py-2">{{ r.dated_on }}</td>
                <td class="border-b border-[#eef1f4] px-3 py-2">{{ r.description }}</td>
                <td class="whitespace-nowrap border-b border-[#eef1f4] px-3 py-2 text-right">{{ r.money_in ?? '' }}</td>
                <td class="whitespace-nowrap border-b border-[#eef1f4] px-3 py-2 text-right">{{ r.money_out ?? '' }}</td>
                <td v-if="showBalanceCol" class="whitespace-nowrap border-b border-[#eef1f4] px-3 py-2 text-right">{{ r.balance ?? '' }}</td>
              </template>
            </tr>
            <tr v-if="!previewRows.length">
              <td :colspan="showBalanceCol ? 5 : 4" class="px-3 py-4 text-center text-fa-muted">No rows to preview.</td>
            </tr>
          </tbody>
        </table>
      </div>

      <p v-if="formError" class="mt-3 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]" role="alert">
        {{ formError }}
      </p>

      <div class="mt-5 flex items-center gap-3">
        <Button label="Import" icon="pi pi-upload" :loading="importing" :disabled="!canImport" @click="doImport" />
        <button type="button" class="font-semibold text-fa-blue hover:underline" @click="chooseDifferentFile">
          Choose a different file
        </button>
        <button type="button" class="font-semibold text-fa-muted hover:underline" @click="backToStatement">
          Cancel
        </button>
      </div>
    </FaCard>

    <!-- Step 1: pick a file -->
    <FaCard v-else title="Upload a statement">
      <p class="mb-4 text-sm text-fa-muted">
        Export your statement from your bank as a CSV (or OFX) file and upload it here — we'll
        auto-detect the columns and let you confirm before importing. One row per transaction;
        re-importing the same file is safe (duplicates are skipped).
      </p>

      <a
        :href="templateHref"
        download="statement_import_template.csv"
        class="mb-5 inline-flex items-center gap-2 rounded-[5px] border border-fa-border bg-white px-3.5 py-2 text-sm font-semibold text-fa-blue hover:bg-[#f7fafc]"
      >
        <i class="pi pi-download" />Download CSV template
      </a>

      <!-- File picker -->
      <input ref="fileInput" type="file" accept=".csv,text/csv,.ofx,application/x-ofx" class="hidden" @change="onFilePicked" />
      <div class="mb-1 flex items-center gap-3">
        <Button label="Choose a file" icon="pi pi-paperclip" severity="secondary" outlined :loading="previewing" @click="fileInput?.click()" />
        <span v-if="file" class="text-sm font-semibold text-fa-text">{{ file.name }}</span>
        <span v-else class="text-sm text-fa-muted">No file chosen</span>
      </div>
      <p v-if="previewing" class="mt-2 text-sm text-fa-muted"><i class="pi pi-spin pi-spinner mr-1" />Reading your file…</p>

      <p v-if="formError" class="mt-3 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]" role="alert">
        {{ formError }}
      </p>

      <div class="mt-5 flex items-center gap-3">
        <button type="button" class="font-semibold text-fa-blue hover:underline" @click="backToStatement">
          Cancel
        </button>
      </div>
    </FaCard>
  </AppLayout>
</template>
