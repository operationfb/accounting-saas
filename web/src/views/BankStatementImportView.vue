<script setup lang="ts">
// Statement import — a dedicated full-page view (not a modal), consistent with the
// manual-entry view. Reached from the statement view's "Upload statement" button
// (owner/admin only — the backend enforces it). Accepts two formats on the same
// endpoint, which the backend auto-detects from the file's contents:
//   - OFX  — the format most banks export directly (no template needed).
//   - CSV  — arranged to match our downloadable template.
// Flow: pick a file → Import (POST …/transactions/import) → show the imported/skipped
// summary → "View statement" returns to /bank-accounts/:id (which refetches).
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import { getBankAccount, importBankStatement } from '@/services/bank-accounts.service'
import type { BankAccount, StatementImportResult } from '@/types/bank-account'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const accountId = String(route.params.id)

const templateHref = `${import.meta.env.BASE_URL}statement_import_template.csv`
const MAX_BYTES = 5 * 1024 * 1024 // mirrors the backend's maxStatementUploadBytes (5 MiB)

// The CSV column guide (single source of truth for "how to fill in our template").
const templateFields = [
  { name: 'date', required: 'Required', format: 'Transaction date, DD/MM/YYYY (e.g. 22/06/2026).' },
  { name: 'description', required: 'Required', format: 'What the transaction was (e.g. Tesco Stores).' },
  { name: 'amount', required: 'Required', format: 'Decimal. Positive for money in, negative (leading -) for money out. e.g. 2500.00 or -54.20.' },
  { name: 'bank_memo', required: 'Optional', format: 'Raw bank narrative / reference (e.g. FPS CREDIT).' },
]

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

// --- file pick + import ---
const fileInput = ref<HTMLInputElement | null>(null)
const file = ref<File | null>(null)
const importing = ref(false)
const formError = ref('')
const result = ref<StatementImportResult | null>(null)

function onFilePicked(e: Event) {
  formError.value = ''
  const picked = (e.target as HTMLInputElement).files?.[0] ?? null
  if (picked && picked.size > MAX_BYTES) {
    formError.value = 'That file is too large (max 5 MB).'
  } else {
    file.value = picked
  }
  ;(e.target as HTMLInputElement).value = '' // allow re-picking the same file
}

async function doImport() {
  if (!file.value || importing.value) return
  importing.value = true
  formError.value = ''
  try {
    result.value = await importBankStatement(accountId, file.value)
  } catch (err) {
    formError.value = (err as ApiError)?.message ?? 'Could not import the statement. Please check the file and try again.'
  } finally {
    importing.value = false
  }
}

function importAnother() {
  result.value = null
  file.value = null
  formError.value = ''
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

    <!-- Upload form -->
    <FaCard v-else title="Upload a statement">
      <p class="mb-4 text-sm text-fa-muted">
        Upload your bank statement. We accept <strong>OFX</strong> — the format most banks export, so
        just download it from your bank and upload it here (no template needed) — or <strong>CSV</strong>
        arranged to match our template below. Re-importing the same file is safe (duplicates are skipped).
      </p>

      <h3 class="mb-2 text-sm font-bold">Importing a CSV</h3>
      <p class="mb-3 text-sm text-fa-muted">Only needed for CSV — if you're uploading an OFX file, skip straight to the upload below.</p>

      <a
        :href="templateHref"
        download="statement_import_template.csv"
        class="mb-5 inline-flex items-center gap-2 rounded-[5px] border border-fa-border bg-white px-3.5 py-2 text-sm font-semibold text-fa-blue hover:bg-[#f7fafc]"
      >
        <i class="pi pi-download" />Download CSV template
      </a>

      <h3 class="mb-2 text-sm font-bold">How to fill in the template</h3>
      <div class="mb-5 overflow-hidden rounded-[5px] border border-fa-border">
        <table class="w-full border-collapse text-[13px]">
          <thead>
            <tr class="bg-[#f7fafc] text-left text-fa-muted">
              <th class="border-b border-fa-border px-3 py-2 font-semibold">Column</th>
              <th class="border-b border-fa-border px-3 py-2 font-semibold">Required</th>
              <th class="border-b border-fa-border px-3 py-2 font-semibold">Format / values</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="f in templateFields" :key="f.name" class="align-top">
              <td class="whitespace-nowrap border-b border-[#eef1f4] px-3 py-2 font-mono">{{ f.name }}</td>
              <td class="whitespace-nowrap border-b border-[#eef1f4] px-3 py-2">{{ f.required }}</td>
              <td class="border-b border-[#eef1f4] px-3 py-2">{{ f.format }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- File picker -->
      <input ref="fileInput" type="file" accept=".ofx,.csv,text/csv,application/x-ofx" class="hidden" @change="onFilePicked" />
      <div class="mb-1 flex items-center gap-3">
        <Button label="Choose file" icon="pi pi-paperclip" severity="secondary" outlined @click="fileInput?.click()" />
        <span v-if="file" class="text-sm font-semibold text-fa-text">{{ file.name }}</span>
        <span v-else class="text-sm text-fa-muted">No file chosen</span>
      </div>

      <p v-if="formError" class="mt-3 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]" role="alert">
        {{ formError }}
      </p>

      <div class="mt-5 flex items-center gap-3">
        <Button label="Import" icon="pi pi-upload" :loading="importing" :disabled="!file" @click="doImport" />
        <button type="button" class="font-semibold text-fa-blue hover:underline" @click="backToStatement">
          Cancel
        </button>
      </div>
    </FaCard>
  </AppLayout>
</template>
