<script setup lang="ts">
// Self-contained "Pushed to FreeAgent ✓ / Failed ⚠" badge for the expense detail
// page. It fetches its OWN data (GET …/expenses/:id/push) and offers Retry / Push
// now for owner/admins. It renders ONLY for owner/admins on an APPROVED expense —
// for anyone/anything else it's empty, so the parent can drop it in unconditionally.
//
// The push happens asynchronously (a Cloud Workflow reacts to the approval), so just
// after approval there's no result row yet → state "none". While connected + none we
// poll a few times to catch the workflow finishing, then fall back to a "Push now".
import { ref, computed, watch, onUnmounted } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { getExpensePushStatus, repushExpense } from '@/services/integrations.service'
import type { FreeAgentPushStatus } from '@/types/integration'
import type { ApiError } from '@/lib/api'

const props = defineProps<{
  expenseId: string
  expenseStatus: string
}>()

const auth = useAuthStore()

// The badge only makes sense for an admin looking at an APPROVED expense (push only
// fires on approval). For everyone/anything else this component renders nothing.
const active = computed(() => auth.isOrgAdmin && props.expenseStatus === 'APPROVED')

const push = ref<FreeAgentPushStatus | null>(null)
const loading = ref(false) // first fetch in flight
const polling = ref(false) // re-polling while the workflow is in flight
const retrying = ref(false)
const errorMsg = ref('')

// Poll bookkeeping for the "Pushing…" window (workflow in flight).
const POLL_MS = 3000
const MAX_POLLS = 10
let pollTimer: ReturnType<typeof setTimeout> | null = null
let polls = 0

function stopPolling() {
  polling.value = false
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

async function fetchStatus() {
  try {
    push.value = await getExpensePushStatus(props.expenseId)
    errorMsg.value = ''
  } catch (err) {
    // 401 is handled globally by apiFetch; a 403 shouldn't happen (we gate on admin).
    // A failed status read just leaves the badge hidden rather than nagging.
    errorMsg.value = (err as ApiError)?.message ?? 'Could not load FreeAgent status.'
  }
}

// Poll while the expense is approved + connected but no result has landed yet.
function maybeSchedulePoll() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
  const p = push.value
  if (!p || !p.connected || p.state !== 'none' || polls >= MAX_POLLS) {
    polling.value = false // resolved (pushed/failed) or gave up — stop
    return
  }
  polling.value = true
  pollTimer = setTimeout(async () => {
    polls += 1
    await fetchStatus()
    maybeSchedulePoll()
  }, POLL_MS)
}

async function load() {
  loading.value = true
  polls = 0
  await fetchStatus()
  loading.value = false
  maybeSchedulePoll()
}

// Manually (re-)push, then drop back into the polling window for the fresh attempt.
async function retry() {
  if (retrying.value) return
  retrying.value = true
  errorMsg.value = ''
  try {
    await repushExpense(props.expenseId)
    polls = 0
    if (push.value) push.value = { ...push.value, state: 'none', error: null, external_url: null }
    maybeSchedulePoll()
  } catch (err) {
    errorMsg.value = (err as ApiError)?.message ?? 'Could not start the push.'
  } finally {
    retrying.value = false
  }
}

// Kick off (and re-kick) whenever the expense becomes an approved one we can see —
// e.g. immediately after an admin approves it (the parent re-fetches → status flips).
watch(
  active,
  (on) => {
    if (on) load()
    else {
      stopPolling()
      push.value = null
    }
  },
  { immediate: true },
)

onUnmounted(stopPolling)

// Single derived display state, so the template stays declarative.
const display = computed<'hidden' | 'pushed' | 'failed' | 'pushing' | 'not_pushed'>(() => {
  if (!active.value) return 'hidden'
  const p = push.value
  if (!p) return loading.value ? 'pushing' : 'hidden' // first fetch
  if (!p.connected) return 'hidden' // FreeAgent isn't set up for this org
  if (p.state === 'pushed') return 'pushed'
  if (p.state === 'failed') return 'failed'
  return polling.value ? 'pushing' : 'not_pushed' // state === 'none'
})
</script>

<template>
  <!-- Pushed: green pill linking to the FreeAgent expense. -->
  <a
    v-if="display === 'pushed'"
    :href="push?.external_url ?? undefined"
    target="_blank"
    rel="noopener"
    class="inline-flex items-center gap-1 rounded-full border border-[#cfe9c7] bg-[#eaf7e6] px-2.5 py-0.5 text-xs font-semibold text-[#3f8038] hover:underline"
    title="View this expense in FreeAgent"
  >
    <i class="pi pi-check text-[10px]" />
    Pushed to FreeAgent
  </a>

  <!-- Failed: red pill (error on hover) + Retry. -->
  <span v-else-if="display === 'failed'" class="inline-flex flex-wrap items-center gap-2">
    <span
      class="inline-flex items-center gap-1 rounded-full border border-[#f6d3d0] bg-[#fdecec] px-2.5 py-0.5 text-xs font-semibold text-[#c0392b]"
      :title="push?.error ?? ''"
    >
      <i class="pi pi-exclamation-triangle text-[10px]" />
      FreeAgent push failed
    </span>
    <button
      type="button"
      class="text-xs font-semibold text-fa-blue hover:underline disabled:opacity-50"
      :disabled="retrying"
      @click="retry"
    >
      {{ retrying ? 'Retrying…' : 'Retry' }}
    </button>
    <span v-if="errorMsg" class="text-xs text-[#c0392b]">{{ errorMsg }}</span>
  </span>

  <!-- Pushing: workflow in flight (or first fetch). -->
  <span
    v-else-if="display === 'pushing'"
    class="inline-flex items-center gap-1 rounded-full border border-[#dde2e8] bg-[#eef1f4] px-2.5 py-0.5 text-xs font-semibold text-[#5b6772]"
  >
    <i class="pi pi-spin pi-spinner text-[10px]" />
    Pushing to FreeAgent…
  </span>

  <!-- Not pushed: gave up waiting → offer a manual push. -->
  <span v-else-if="display === 'not_pushed'" class="inline-flex flex-wrap items-center gap-2">
    <span
      class="inline-flex items-center gap-1 rounded-full border border-[#dde2e8] bg-[#eef1f4] px-2.5 py-0.5 text-xs font-semibold text-[#5b6772]"
    >
      Not pushed to FreeAgent
    </span>
    <button
      type="button"
      class="text-xs font-semibold text-fa-blue hover:underline disabled:opacity-50"
      :disabled="retrying"
      @click="retry"
    >
      {{ retrying ? 'Pushing…' : 'Push now' }}
    </button>
    <span v-if="errorMsg" class="text-xs text-[#c0392b]">{{ errorMsg }}</span>
  </span>
</template>
