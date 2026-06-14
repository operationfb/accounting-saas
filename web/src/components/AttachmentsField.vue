<script setup lang="ts">
// Staged receipt-attachment manager used by ExpenseEntryView in BOTH modes.
//
// The whole point of this component: attachment changes are STAGED locally and
// only committed when the parent calls commit(expenseId) — which the parent does
// after the expense exists (create) or has been saved (edit). That keeps create
// and edit consistent and lets "Cancel" discard everything.
//
//   - create mode (no expenseId): the list is just newly-picked files.
//   - edit mode (expenseId set): existing files are loaded on mount; the user can
//     mark them for removal (Undo until save), add new files, and change which is
//     primary — nothing hits the server until commit().
//
// The parent gets commit() + hasPendingChanges() via defineExpose().
import { ref, computed, onMounted } from 'vue'
import Button from 'primevue/button'
import InputText from 'primevue/inputtext'
import RadioButton from 'primevue/radiobutton'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import {
  listAttachments,
  uploadAttachment,
  deleteAttachment,
  setPrimaryAttachment,
  getDownloadUrl,
} from '@/services/attachments.service'
import type { Attachment } from '@/types/attachment'
import { formatBytes } from '@/lib/format'
import type { ApiError } from '@/lib/api'

const props = defineProps<{
  // The expense to manage attachments for. Undefined in create mode (the expense
  // doesn't exist yet) — the parent passes the new id to commit() instead.
  expenseId?: string
}>()

// Result the parent reads after commit(): whether everything applied, and counts
// for an optional "N file(s) didn't upload" message. (Local, not exported —
// <script setup> can't export; the parent gets the shape via InstanceType.)
interface AttachmentsCommitResult {
  ok: boolean
  uploaded: number
  deleted: number
  message?: string
}

// A row is either an EXISTING server attachment (edit mode) or a NEW staged file
// not yet uploaded. `key` is a stable id for :key and the primary radio group.
interface ExistingRow {
  key: string
  attachment: Attachment
  remove: boolean // marked for deletion; not deleted until commit()
}
interface StagedRow {
  key: string
  file: File
  description: string // sent with the upload (optional)
}

// --- client-side validation rules (early UX only — the backend re-sniffs the
// real MIME type and re-checks the size, and is the final authority) ---
const ACCEPTED_TYPES = ['image/jpeg', 'image/png', 'application/pdf']
const ACCEPT_ATTR = '.pdf,.jpg,.jpeg,.png,application/pdf,image/jpeg,image/png'
const EXT_TO_TYPE: Record<string, string> = {
  pdf: 'application/pdf',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  png: 'image/png',
}
const parsedMax = Number(import.meta.env.VITE_MAX_UPLOAD_MB)
const MAX_MB = Number.isFinite(parsedMax) && parsedMax > 0 ? parsedMax : 20
const MAX_BYTES = MAX_MB * 1024 * 1024

// --- state ---
const existing = ref<ExistingRow[]>([])
const staged = ref<StagedRow[]>([])
const primaryKey = ref('') // key of the row chosen as primary (among selectable)
const originalPrimaryKey = ref('') // what the server had on load — to detect a change

const loadingExisting = ref(false)
const existingError = ref('')
const addErrors = ref<string[]>([]) // per-file rejection notes from the last pick
const previewError = ref('')

const fileInput = ref<HTMLInputElement | null>(null)

let uid = 0
const nextKey = () => `s-${++uid}` // staged-row keys; existing rows use `e-${id}`

// Keys eligible to be primary = existing-not-removed + all staged, in display
// order (existing first, then staged).
const selectableKeys = computed(() => [
  ...existing.value.filter((r) => !r.remove).map((r) => r.key),
  ...staged.value.map((r) => r.key),
])

const isEmpty = computed(
  () => existing.value.length === 0 && staged.value.length === 0 && !loadingExisting.value,
)

// Keep `primaryKey` pointing at a real, selectable row. Called after any add /
// remove so a removed primary auto-moves to the first remaining file.
function ensurePrimaryValid() {
  if (!selectableKeys.value.includes(primaryKey.value)) {
    primaryKey.value = selectableKeys.value[0] ?? ''
  }
}

// --- load existing (edit mode) ---
async function loadExisting(id: string) {
  loadingExisting.value = true
  existingError.value = ''
  try {
    const list = await listAttachments(id)
    existing.value = list.map((a) => ({ key: `e-${a.id}`, attachment: a, remove: false }))
    // NB: don't touch `staged` here — successful uploads already remove themselves
    // from it, so on a partial-failure re-sync the still-pending files survive.
    const primary = list.find((a) => a.is_primary)
    primaryKey.value = primary ? `e-${primary.id}` : (selectableKeys.value[0] ?? '')
    originalPrimaryKey.value = primaryKey.value
  } catch (err) {
    existingError.value = (err as ApiError)?.message ?? 'Could not load attachments.'
  } finally {
    loadingExisting.value = false
  }
}

onMounted(() => {
  if (props.expenseId) loadExisting(props.expenseId)
})

// --- adding files ---
// Real MIME type, falling back to the extension when the browser gives none.
function effectiveType(file: File): string {
  if (ACCEPTED_TYPES.includes(file.type)) return file.type
  const ext = file.name.split('.').pop()?.toLowerCase() ?? ''
  return EXT_TO_TYPE[ext] ?? file.type ?? ''
}

function validateFile(file: File): string {
  const type = effectiveType(file)
  if (!ACCEPTED_TYPES.includes(type)) return `${file.name} — unsupported type (PDF, JPEG or PNG only).`
  if (file.size <= 0) return `${file.name} — the file is empty.`
  if (file.size > MAX_BYTES) return `${file.name} — larger than ${MAX_MB} MB.`
  return ''
}

function onFilesPicked(e: Event) {
  const input = e.target as HTMLInputElement
  addErrors.value = []
  for (const file of Array.from(input.files ?? [])) {
    const err = validateFile(file)
    if (err) {
      addErrors.value.push(err)
      continue
    }
    staged.value.push({ key: nextKey(), file, description: '' })
  }
  input.value = '' // reset so picking the same file again still fires `change`
  ensurePrimaryValid()
}

function removeStaged(key: string) {
  staged.value = staged.value.filter((r) => r.key !== key)
  ensurePrimaryValid()
}

function toggleRemoveExisting(row: ExistingRow) {
  row.remove = !row.remove
  ensurePrimaryValid()
}

// --- preview (open in a new tab) ---
// Existing files: fetch a short-lived signed URL, then point the tab at it. We
// open the blank tab SYNCHRONOUSLY (inside the click) so the pop-up blocker
// allows it, then set its location once the async URL arrives.
function openExisting(att: Attachment) {
  if (!props.expenseId) return
  previewError.value = ''
  const w = window.open('', '_blank')
  getDownloadUrl(props.expenseId, att.id)
    .then((url) => {
      if (w) w.location.href = url
      else window.location.href = url // pop-up blocked → navigate this tab
    })
    .catch((err) => {
      if (w) w.close()
      previewError.value = (err as ApiError)?.message ?? 'Could not open this file.'
    })
}

// Staged files aren't uploaded yet — preview straight from the local File.
function openStaged(file: File) {
  const url = URL.createObjectURL(file)
  window.open(url, '_blank')
  // Revoke later so the new tab has time to load it first.
  setTimeout(() => URL.revokeObjectURL(url), 60_000)
}

// --- display helpers ---
function iconFor(type: string): string {
  if (type === 'application/pdf') return 'pi pi-file-pdf'
  if (type.startsWith('image/')) return 'pi pi-image'
  return 'pi pi-file'
}

// --- exposed to the parent ---
function hasPendingChanges(): boolean {
  return (
    staged.value.length > 0 ||
    existing.value.some((r) => r.remove) ||
    primaryKey.value !== originalPrimaryKey.value
  )
}

// Resolve the server id of the row currently chosen as primary — an existing
// attachment's id, or (for a staged file) the id returned from its upload.
function resolvePrimaryServerId(uploadedIdByKey: Map<string, string>): string | undefined {
  const key = primaryKey.value
  if (!key) return undefined
  if (key.startsWith('e-')) return existing.value.find((r) => r.key === key)?.attachment.id
  return uploadedIdByKey.get(key)
}

// Apply the staged change-set against a now-existing expense. Runs sequentially
// and mutates state as each step succeeds, so on a partial failure the component
// still reflects what actually committed; it then re-syncs from the server.
async function commit(expenseId: string): Promise<AttachmentsCommitResult> {
  const toDelete = existing.value.filter((r) => r.remove)
  const toUpload = [...staged.value]
  const primaryChanged = primaryKey.value !== originalPrimaryKey.value

  // Nothing staged → nothing to do.
  if (toDelete.length === 0 && toUpload.length === 0 && !primaryChanged) {
    return { ok: true, uploaded: 0, deleted: 0 }
  }

  let uploaded = 0
  let deleted = 0
  const uploadedIdByKey = new Map<string, string>()

  try {
    // 1) Deletions first (frees up the "primary" slot the backend may re-promote).
    for (const row of toDelete) {
      await deleteAttachment(expenseId, row.attachment.id)
      existing.value = existing.value.filter((r) => r !== row)
      deleted++
    }
    // 2) Uploads, remembering each new server id for the primary reconcile.
    for (const row of toUpload) {
      const created = await uploadAttachment(expenseId, row.file, row.description)
      uploadedIdByKey.set(row.key, created.id)
      staged.value = staged.value.filter((r) => r !== row)
      uploaded++
    }
    // 3) Reconcile primary. Skip when only one file will remain (it's primary by
    //    construction). setPrimary is idempotent, so a redundant set is harmless.
    const total = existing.value.length + uploaded
    const desiredId = resolvePrimaryServerId(uploadedIdByKey)
    if (desiredId && total > 1) {
      await setPrimaryAttachment(expenseId, desiredId)
    }

    await loadExisting(expenseId) // re-sync to canonical server state
    return { ok: true, uploaded, deleted }
  } catch (err) {
    // Re-sync so the UI matches whatever actually committed before the failure.
    await loadExisting(expenseId).catch(() => {})
    return {
      ok: false,
      uploaded,
      deleted,
      message: (err as ApiError)?.message ?? 'Some attachment changes could not be saved.',
    }
  }
}

// Clear all staged state — used by "Create and add another" so a fresh expense
// starts with an empty file list (even if the previous commit left a failed file
// behind).
function reset() {
  existing.value = []
  staged.value = []
  primaryKey.value = ''
  originalPrimaryKey.value = ''
  addErrors.value = []
  existingError.value = ''
  previewError.value = ''
}

defineExpose({ commit, hasPendingChanges, reset })
</script>

<template>
  <FaCard title="Attachment" :note="`PDF, JPEG or PNG · max ${MAX_MB} MB`">
    <FormRow label="Files">
      <div class="flex items-center gap-2.5">
        <Button
          type="button"
          label="Add files"
          icon="pi pi-paperclip"
          severity="secondary"
          outlined
          @click="fileInput?.click()"
        />
        <span class="text-sm text-fa-muted">Attach one or more receipts.</span>
      </div>
      <!-- hidden native input; the styled button above proxies to it -->
      <input
        ref="fileInput"
        type="file"
        multiple
        :accept="ACCEPT_ATTR"
        class="hidden"
        @change="onFilesPicked"
      />

      <!-- per-file rejection notes from the last pick -->
      <ul v-if="addErrors.length" class="mt-1 space-y-0.5">
        <li v-for="(msg, i) in addErrors" :key="i" class="text-xs text-[#c0392b]">{{ msg }}</li>
      </ul>
    </FormRow>

    <!-- existing-list states (edit mode) -->
    <div v-if="loadingExisting" class="py-3 text-sm text-fa-muted">
      <i class="pi pi-spin pi-spinner mr-2" />Loading attachments…
    </div>
    <p v-else-if="existingError" class="py-2 text-xs text-[#c0392b]">
      {{ existingError }}
      <button type="button" class="underline" @click="props.expenseId && loadExisting(props.expenseId)">
        Retry
      </button>
    </p>

    <p v-if="previewError" class="py-1 text-xs text-[#c0392b]">{{ previewError }}</p>

    <!-- the unified file list: existing rows first, then staged rows -->
    <ul v-if="!loadingExisting && (existing.length || staged.length)" class="mt-1">
      <!-- EXISTING (server) files -->
      <li
        v-for="row in existing"
        :key="row.key"
        class="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-[#eef1f4] py-2 last:border-b-0"
        :class="{ 'opacity-50': row.remove }"
      >
        <RadioButton
          v-model="primaryKey"
          :value="row.key"
          :input-id="`pri-${row.key}`"
          :disabled="row.remove"
          title="Set as the primary receipt"
        />
        <i :class="iconFor(row.attachment.content_type)" class="text-fa-muted" />
        <button
          type="button"
          class="font-semibold text-fa-blue hover:underline"
          :class="{ 'line-through': row.remove }"
          @click="openExisting(row.attachment)"
        >
          {{ row.attachment.file_name }}
        </button>
        <span class="text-xs text-fa-muted">{{ formatBytes(row.attachment.file_size_bytes) }}</span>
        <span
          v-if="primaryKey === row.key && !row.remove"
          class="rounded bg-[#eaf7e6] px-1.5 py-0.5 text-[11px] font-semibold text-[#3f8038]"
        >
          Primary
        </span>
        <span v-if="row.remove" class="text-xs italic text-fa-muted">Will be removed on save</span>
        <span
          v-if="row.attachment.description && !row.remove"
          class="basis-full pl-7 text-xs text-fa-muted"
        >
          {{ row.attachment.description }}
        </span>
        <button
          type="button"
          class="ml-auto text-sm font-semibold text-fa-blue hover:underline"
          @click="toggleRemoveExisting(row)"
        >
          {{ row.remove ? 'Undo' : 'Remove' }}
        </button>
      </li>

      <!-- STAGED (new, not-yet-uploaded) files -->
      <li
        v-for="row in staged"
        :key="row.key"
        class="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-[#eef1f4] py-2 last:border-b-0"
      >
        <RadioButton
          v-model="primaryKey"
          :value="row.key"
          :input-id="`pri-${row.key}`"
          title="Set as the primary receipt"
        />
        <i :class="iconFor(effectiveType(row.file))" class="text-fa-muted" />
        <button
          type="button"
          class="font-semibold text-fa-blue hover:underline"
          @click="openStaged(row.file)"
        >
          {{ row.file.name }}
        </button>
        <span class="text-xs text-fa-muted">{{ formatBytes(row.file.size) }}</span>
        <span
          v-if="primaryKey === row.key"
          class="rounded bg-[#eaf7e6] px-1.5 py-0.5 text-[11px] font-semibold text-[#3f8038]"
        >
          Primary
        </span>
        <span class="rounded bg-[#fff4e0] px-1.5 py-0.5 text-[11px] font-semibold text-[#a86a00]">
          Pending upload
        </span>
        <button
          type="button"
          class="ml-auto text-sm font-semibold text-fa-blue hover:underline"
          @click="removeStaged(row.key)"
        >
          Remove
        </button>
        <div class="basis-full pl-7">
          <InputText
            v-model="row.description"
            placeholder="Description (optional)"
            class="w-full max-w-md"
            size="small"
          />
        </div>
      </li>
    </ul>

    <p v-if="isEmpty" class="py-2 text-sm text-fa-muted">No files attached yet.</p>
  </FaCard>
</template>
