<script setup lang="ts">
// Self-contained "Pushed to FreeAgent ✓ / Failed ⚠" badge for the expense detail
// page. It fetches its OWN data (GET …/expenses/:id/push) and offers Retry / Push
// now for owner/admins. It renders ONLY for owner/admins on an APPROVED expense —
// for anyone/anything else it's empty, so the parent can drop it in unconditionally.
//
// The push happens asynchronously (a Cloud Workflow reacts to the approval and
// later writes the result back to OUR backend — there is no live callback to the
// browser), so the badge POLLS. Two cases need polling:
//   1. just approved → no result row yet (state "none") → wait for the first result.
//   2. Retry/Push now → the backend STILL returns the previous terminal row
//      (failed/pushed) until the workflow overwrites it. So we must NOT stop on that
//      stale value — we keep "Pushing…" and poll until a NEW result lands, detected
//      by the result's `pushed_at` timestamp changing from the one we're superseding.
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
const awaiting = ref(false) // a push is in flight (just-approved or retry) — show "Pushing…", keep polling
const retrying = ref(false) // the Retry/Push-now POST itself is in flight
const errorMsg = ref('')

// Poll cadence + budget for the "Pushing…" window. The workflow round-trip
// (Pub/Sub → Eventarc → workflow → FreeAgent → callback) can take a while, so the
// window is generous; if it elapses we fall back to the latest known state.
const POLL_MS = 3000
const MAX_POLLS = 15
let pollTimer: ReturnType<typeof setTimeout> | null = null
let polls = 0
// The `pushed_at` of the result we're waiting to supersede. null = we're waiting for
// the FIRST result (nothing pushed yet), so any terminal result counts as new.
let baselineStamp: string | null | undefined = null

function clearTimer() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

function stopPolling() {
  awaiting.value = false
  clearTimer()
}

// Is `fresh` a NEW outcome (vs the baseline we're waiting past)? A "none" never
// counts; with no baseline any terminal result is new; otherwise the row must have
// been rewritten (its pushed_at changed).
function isNewResult(fresh: FreeAgentPushStatus): boolean {
  if (fresh.state === 'none') return false
  if (!baselineStamp) return true
  return fresh.pushed_at !== baselineStamp
}

async function fetchStatus(): Promise<FreeAgentPushStatus | null> {
  try {
    const fresh = await getExpensePushStatus(props.expenseId)
    errorMsg.value = ''
    return fresh
  } catch (err) {
    // 401 is handled globally by apiFetch; a 403 shouldn't happen (we gate on admin).
    errorMsg.value = (err as ApiError)?.message ?? 'Could not load FreeAgent status.'
    return null
  }
}

function scheduleNextPoll() {
  clearTimer()
  if (polls >= MAX_POLLS) {
    stopPolling() // gave up — display falls back to the latest known push.value
    return
  }
  pollTimer = setTimeout(async () => {
    polls += 1
    const fresh = await fetchStatus()
    if (fresh && (isNewResult(fresh) || !fresh.connected)) {
      // A genuinely new outcome (or the integration vanished) → apply it and stop.
      push.value = fresh
      stopPolling()
      return
    }
    // Stale (the old row hasn't been overwritten yet) → keep "Pushing…", keep polling.
    scheduleNextPoll()
  }, POLL_MS)
}

// Begin awaiting a result newer than `baseline` (null = the first result after approval).
function startAwaiting(baseline: string | null | undefined) {
  baselineStamp = baseline
  polls = 0
  awaiting.value = true
  scheduleNextPoll()
}

async function load() {
  loading.value = true
  const fresh = await fetchStatus()
  loading.value = false
  if (!fresh) return
  push.value = fresh
  // Approved + connected but nothing landed yet → wait for the first result.
  if (fresh.connected && fresh.state === 'none') startAwaiting(null)
}

// Manually (re-)push. Keep showing the current pill until the POST returns, then
// switch to "Pushing…" and poll for the NEW result (newer pushed_at than now).
async function retry() {
  if (retrying.value) return
  retrying.value = true
  errorMsg.value = ''
  try {
    await repushExpense(props.expenseId)
    startAwaiting(push.value?.pushed_at ?? null)
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
  if (awaiting.value || (loading.value && !push.value)) return 'pushing' // in flight
  const p = push.value
  if (!p || !p.connected) return 'hidden' // FreeAgent isn't set up for this org
  if (p.state === 'pushed') return 'pushed'
  if (p.state === 'failed') return 'failed'
  return 'not_pushed' // state none, not awaiting → gave up / never attempted
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
