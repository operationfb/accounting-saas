<script setup lang="ts">
// IntegrationsView — owner/admin screen to connect/disconnect third-party
// integrations. Currently supports HMRC Making Tax Digital and FreeAgent.
//
// HMRC card appears ABOVE FreeAgent. The layout for each integration is identical:
//   - !has_credentials  → operator hasn't configured the app yet (info note)
//   - has_credentials && !connected → grey "Not connected" badge + Connect button
//   - connected         → green "Connected" badge + connected_at + Disconnect button
//
// The backend OAuth callback redirects to /settings/integrations with a query param:
//   ?hmrc=connected | ?hmrc=error&reason=…
//   ?freeagent=connected | ?freeagent=error&reason=…
// consumeCallbackQuery() reads both and shows the shared banner once.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import Dialog from 'primevue/dialog'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import { useAuthStore } from '@/stores/auth'
import {
  getFreeAgentStatus,
  disconnectFreeAgent,
  getFreeAgentConnectUrl,
  getHmrcStatus,
  disconnectHmrc,
  getHmrcConnectUrl,
} from '@/services/integrations.service'
import { checkHmrcPeriods, syncHmrcPeriods } from '@/services/vat.service'
import type { FreeAgentStatus, HmrcStatus } from '@/types/integration'
import type { VatPeriodCheck } from '@/types/vat'
import { formatDateTime, formatDate } from '@/lib/format'
import type { ApiError } from '@/lib/api'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const canManage = computed(() => auth.isOrgAdmin)

// --- shared banner (OAuth callback result or disconnect feedback) ---
const banner = reactive<{ type: 'success' | 'error' | ''; message: string }>({ type: '', message: '' })

// Reason messages for each provider's OAuth error callback.
const FA_REASON_MESSAGES: Record<string, string> = {
  invalid_state: "The connection link was invalid or expired. Please try connecting again.",
  missing_code: "FreeAgent did not return an authorisation code. Please try again.",
  not_configured: "FreeAgent is not set up yet. Please contact your administrator.",
  exchange_failed: "FreeAgent rejected the connection. Please try again, or contact your administrator.",
  internal: "Something went wrong completing the FreeAgent connection. Please try again.",
}
const HMRC_REASON_MESSAGES: Record<string, string> = {
  invalid_state: "The connection link was invalid or expired. Please try connecting again.",
  missing_code: "HMRC did not return an authorisation code. Please try again.",
  not_configured: "HMRC is not set up yet. Please contact your administrator.",
  exchange_failed: "HMRC rejected the connection. Please try again, or contact your administrator.",
  internal: "Something went wrong completing the HMRC connection. Please try again.",
}

// Read the callback query params once, show a banner, then strip them so a
// refresh doesn't re-show it.
function consumeCallbackQuery() {
  const hmrcResult = route.query.hmrc
  const faResult = route.query.freeagent

  if (hmrcResult === 'connected') {
    banner.type = 'success'
    banner.message = 'Connected to HMRC Making Tax Digital.'
  } else if (hmrcResult === 'error') {
    const reason = typeof route.query.reason === 'string' ? route.query.reason : ''
    banner.type = 'error'
    banner.message = HMRC_REASON_MESSAGES[reason] ?? 'Could not connect to HMRC. Please try again.'
  } else if (faResult === 'connected') {
    banner.type = 'success'
    banner.message = 'Connected to FreeAgent.'
  } else if (faResult === 'error') {
    const reason = typeof route.query.reason === 'string' ? route.query.reason : ''
    banner.type = 'error'
    banner.message = FA_REASON_MESSAGES[reason] ?? 'Could not connect to FreeAgent. Please try again.'
  } else {
    return
  }
  void router.replace({ query: {} })
}

// =============================================================================
// HMRC state + actions
// =============================================================================
const hmrcLoading = ref(true)
const hmrcLoadError = ref('')
const hmrcStatus = ref<HmrcStatus | null>(null)
const hmrcConnecting = ref(false)
const hmrcDisconnecting = ref(false)
const hmrcActionError = ref('')

async function loadHmrc() {
  hmrcLoading.value = true
  hmrcLoadError.value = ''
  try {
    hmrcStatus.value = await getHmrcStatus()
  } catch (err) {
    hmrcLoadError.value = (err as ApiError)?.message ?? 'Could not load your HMRC settings.'
  } finally {
    hmrcLoading.value = false
  }
}

async function connectHmrc() {
  if (hmrcConnecting.value) return
  hmrcActionError.value = ''
  hmrcConnecting.value = true
  try {
    const url = await getHmrcConnectUrl()
    window.location.assign(url)
  } catch (err) {
    hmrcActionError.value = (err as ApiError)?.message ?? 'Could not start the HMRC connection.'
    hmrcConnecting.value = false
  }
}

async function disconnectHmrcOrg() {
  if (hmrcDisconnecting.value) return
  hmrcActionError.value = ''
  banner.type = ''
  hmrcDisconnecting.value = true
  try {
    await disconnectHmrc()
    await loadHmrc()
    banner.type = 'success'
    banner.message = 'Disconnected from HMRC. You can reconnect anytime.'
  } catch (err) {
    hmrcActionError.value = (err as ApiError)?.message ?? 'Could not disconnect.'
  } finally {
    hmrcDisconnecting.value = false
  }
}

// --- HMRC period reconciliation (runs once, right after a fresh connect) ---
// If our generated VAT periods don't line up with HMRC's obligations, offer to
// rewrite the settings to match. Accept → sync; Reject → drop the connection
// (the user asked that a rejection NOT keep the connection).
const reconcileVisible = ref(false)
const periodCheck = ref<VatPeriodCheck | null>(null)
const reconcileBusy = ref(false)
const reconcileError = ref('')

// Title-case a frequency code ("quarterly" → "Quarterly"); em dash when unset.
function freqLabel(f?: string | null): string {
  return f ? f.charAt(0).toUpperCase() + f.slice(1) : '—'
}

async function runHmrcReconcile() {
  try {
    const check = await checkHmrcPeriods()
    if (check.applicable && !check.matches) {
      periodCheck.value = check
      reconcileError.value = ''
      reconcileVisible.value = true
    }
  } catch {
    // Best-effort: a failed check must never block or undo the connection.
  }
}

async function acceptReconcile() {
  if (reconcileBusy.value) return
  reconcileError.value = ''
  reconcileBusy.value = true
  try {
    await syncHmrcPeriods()
    reconcileVisible.value = false
    banner.type = 'success'
    banner.message = 'Connected to HMRC. Your VAT periods were updated to match HMRC.'
  } catch (err) {
    reconcileError.value = (err as ApiError)?.message ?? 'Could not update your VAT periods.'
  } finally {
    reconcileBusy.value = false
  }
}

async function rejectReconcile() {
  if (reconcileBusy.value) return
  reconcileError.value = ''
  reconcileBusy.value = true
  try {
    await disconnectHmrc() // reject = do not keep the connection
    reconcileVisible.value = false
    await loadHmrc()
    banner.type = 'error'
    banner.message = 'Connection cancelled — your VAT periods did not match HMRC.'
  } catch (err) {
    reconcileError.value = (err as ApiError)?.message ?? 'Could not cancel the connection.'
  } finally {
    reconcileBusy.value = false
  }
}

// =============================================================================
// FreeAgent state + actions
// =============================================================================
const faLoading = ref(true)
const faLoadError = ref('')
const faStatus = ref<FreeAgentStatus | null>(null)
const faConnecting = ref(false)
const faDisconnecting = ref(false)
const faActionError = ref('')

async function loadFa() {
  faLoading.value = true
  faLoadError.value = ''
  try {
    faStatus.value = await getFreeAgentStatus()
  } catch (err) {
    faLoadError.value = (err as ApiError)?.message ?? 'Could not load your FreeAgent settings.'
  } finally {
    faLoading.value = false
  }
}

async function connectFa() {
  if (faConnecting.value) return
  faActionError.value = ''
  faConnecting.value = true
  try {
    const url = await getFreeAgentConnectUrl()
    window.location.assign(url)
  } catch (err) {
    faActionError.value = (err as ApiError)?.message ?? 'Could not start the FreeAgent connection.'
    faConnecting.value = false
  }
}

async function disconnectFa() {
  if (faDisconnecting.value) return
  faActionError.value = ''
  banner.type = ''
  faDisconnecting.value = true
  try {
    await disconnectFreeAgent()
    await loadFa()
    banner.type = 'success'
    banner.message = 'Disconnected from FreeAgent. You can reconnect anytime.'
  } catch (err) {
    faActionError.value = (err as ApiError)?.message ?? 'Could not disconnect.'
  } finally {
    faDisconnecting.value = false
  }
}

onMounted(() => {
  // Capture this BEFORE consumeCallbackQuery strips the query params.
  const justConnectedHmrc = route.query.hmrc === 'connected'
  consumeCallbackQuery()
  if (canManage.value) {
    loadHmrc()
    loadFa()
    // Only reconcile right after a fresh HMRC connect — not on every visit.
    if (justConnectedHmrc) runHmrcReconcile()
  } else {
    hmrcLoading.value = false
    faLoading.value = false
  }
})
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">Integrations</h1>

    <!-- Non-admin -->
    <template v-if="!canManage">
      <FaCard title="HMRC Making Tax Digital" class="mb-4">
        <div
          class="rounded border border-fa-border bg-fa-card-header px-3 py-2 text-sm text-fa-muted"
          role="note"
        >
          Only owners and admins can manage integrations.
        </div>
      </FaCard>
      <FaCard title="FreeAgent">
        <div
          class="rounded border border-fa-border bg-fa-card-header px-3 py-2 text-sm text-fa-muted"
          role="note"
        >
          Only owners and admins can manage integrations.
        </div>
      </FaCard>
    </template>

    <template v-else>
      <!-- Shared banner (OAuth callback result / disconnect feedback) -->
      <div
        v-if="banner.type === 'success'"
        class="mb-4 rounded border border-[#cfe9c7] bg-[#eaf7e6] px-3 py-2 text-sm text-[#3f8038]"
        role="status"
      >
        {{ banner.message }}
      </div>
      <div
        v-else-if="banner.type === 'error'"
        class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
        role="alert"
      >
        {{ banner.message }}
      </div>

      <!-- ================================================================ -->
      <!-- HMRC Making Tax Digital card (above FreeAgent)                  -->
      <!-- ================================================================ -->
      <FaCard title="HMRC Making Tax Digital" class="mb-4">
        <!-- Loading -->
        <div v-if="hmrcLoading" class="py-10 text-center text-fa-muted">
          <i class="pi pi-spin pi-spinner mr-2" />Loading…
        </div>

        <!-- Load error -->
        <div v-else-if="hmrcLoadError" class="py-8 text-center">
          <p class="mb-4 text-sm text-[#c0392b]">{{ hmrcLoadError }}</p>
          <Button label="Try again" severity="secondary" outlined @click="loadHmrc" />
        </div>

        <template v-else-if="hmrcStatus">
          <p class="mb-4 text-sm text-fa-muted">
            Connect this organisation to HMRC's Making Tax Digital so VAT returns can be submitted
            electronically directly from this application.
          </p>

          <div
            v-if="hmrcActionError"
            class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
            role="alert"
          >
            {{ hmrcActionError }}
          </div>

          <!-- CONNECTED -->
          <div v-if="hmrcStatus.connected" class="space-y-3">
            <div class="flex flex-wrap items-center gap-2 text-sm">
              <span
                class="inline-block rounded-full border border-[#cfe9c7] bg-[#eaf7e6] px-2.5 py-0.5 text-xs font-semibold text-[#3f8038]"
                >Connected</span
              >
              <span v-if="hmrcStatus.connected_at" class="text-fa-muted"
                >since {{ formatDateTime(hmrcStatus.connected_at) }}</span
              >
            </div>
            <Button
              label="Disconnect"
              severity="danger"
              outlined
              :loading="hmrcDisconnecting"
              @click="disconnectHmrcOrg"
            />
          </div>

          <!-- CONFIGURED, NOT CONNECTED -->
          <div v-else-if="hmrcStatus.has_credentials" class="space-y-3">
            <div class="flex flex-wrap items-center gap-2 text-sm">
              <span
                class="inline-block rounded-full border border-[#dde2e8] bg-[#eef1f4] px-2.5 py-0.5 text-xs font-semibold text-[#5b6772]"
                >Not connected</span
              >
            </div>
            <Button
              label="Connect to HMRC"
              icon="pi pi-link"
              :loading="hmrcConnecting"
              @click="connectHmrc"
            />
          </div>

          <!-- NOT CONFIGURED (no global app credentials in the DB yet) -->
          <div
            v-else
            class="rounded border border-fa-border bg-fa-card-header px-3 py-2 text-sm text-fa-muted"
            role="note"
          >
            HMRC is not set up yet. Once your administrator has configured it, you will be able to
            connect this organisation here.
          </div>
        </template>
      </FaCard>

      <!-- ================================================================ -->
      <!-- FreeAgent card                                                   -->
      <!-- ================================================================ -->
      <FaCard title="FreeAgent">
        <!-- Loading -->
        <div v-if="faLoading" class="py-10 text-center text-fa-muted">
          <i class="pi pi-spin pi-spinner mr-2" />Loading…
        </div>

        <!-- Load error -->
        <div v-else-if="faLoadError" class="py-8 text-center">
          <p class="mb-4 text-sm text-[#c0392b]">{{ faLoadError }}</p>
          <Button label="Try again" severity="secondary" outlined @click="loadFa" />
        </div>

        <template v-else-if="faStatus">
          <p class="mb-4 text-sm text-fa-muted">
            Connect this organisation to FreeAgent so approved expenses are pushed automatically. If a
            push fails you can re-push it from the expense.
          </p>

          <div
            v-if="faActionError"
            class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
            role="alert"
          >
            {{ faActionError }}
          </div>

          <!-- CONNECTED -->
          <div v-if="faStatus.connected" class="space-y-3">
            <div class="flex flex-wrap items-center gap-2 text-sm">
              <span
                class="inline-block rounded-full border border-[#cfe9c7] bg-[#eaf7e6] px-2.5 py-0.5 text-xs font-semibold text-[#3f8038]"
                >Connected</span
              >
              <span v-if="faStatus.connected_at" class="text-fa-muted"
                >since {{ formatDateTime(faStatus.connected_at) }}</span
              >
            </div>
            <Button
              label="Disconnect"
              severity="danger"
              outlined
              :loading="faDisconnecting"
              @click="disconnectFa"
            />
          </div>

          <!-- CONFIGURED, NOT CONNECTED -->
          <div v-else-if="faStatus.has_credentials" class="space-y-3">
            <div class="flex flex-wrap items-center gap-2 text-sm">
              <span
                class="inline-block rounded-full border border-[#dde2e8] bg-[#eef1f4] px-2.5 py-0.5 text-xs font-semibold text-[#5b6772]"
                >Not connected</span
              >
            </div>
            <Button
              label="Connect to FreeAgent"
              icon="pi pi-link"
              :loading="faConnecting"
              @click="connectFa"
            />
          </div>

          <!-- NOT CONFIGURED (no global app credentials in the DB yet) -->
          <div
            v-else
            class="rounded border border-fa-border bg-fa-card-header px-3 py-2 text-sm text-fa-muted"
            role="note"
          >
            FreeAgent is not set up yet. Once your administrator has configured it, you will be able to
            connect this organisation here.
          </div>
        </template>
      </FaCard>
    </template>

    <!-- ============ HMRC PERIOD RECONCILIATION (post-connect) ============ -->
    <!-- Shown when, right after connecting, our generated VAT periods don't line
         up with HMRC's obligations. Accept → rewrite settings; Reject → disconnect.
         Not closable: the user must choose (reject is the "do not connect" path). -->
    <Dialog
      v-model:visible="reconcileVisible"
      modal
      header="Your VAT periods don't match HMRC"
      :style="{ width: '34rem' }"
      :closable="false"
    >
      <div v-if="periodCheck" class="flex flex-col gap-4 text-sm">
        <p>
          The VAT periods in your settings don't match HMRC's records. Would you like to adjust
          Kontala's VAT settings to match HMRC?
        </p>

        <div class="overflow-hidden rounded border border-fa-border">
          <div class="grid grid-cols-3 bg-fa-card-header px-3 py-2 text-xs font-semibold text-fa-muted">
            <span></span>
            <span>Your settings</span>
            <span>HMRC</span>
          </div>
          <div class="grid grid-cols-3 border-t border-fa-border px-3 py-2">
            <span class="text-fa-muted">Frequency</span>
            <span>{{ freqLabel(periodCheck.current.return_frequency) }}</span>
            <span class="font-semibold">{{ freqLabel(periodCheck.suggested.return_frequency) }}</span>
          </div>
          <div class="grid grid-cols-3 border-t border-fa-border px-3 py-2">
            <span class="text-fa-muted">First period ends</span>
            <span>{{
              periodCheck.current.first_return_period_end
                ? formatDate(periodCheck.current.first_return_period_end)
                : '—'
            }}</span>
            <span class="font-semibold">{{
              periodCheck.suggested.first_return_period_end
                ? formatDate(periodCheck.suggested.first_return_period_end)
                : '—'
            }}</span>
          </div>
        </div>

        <div
          v-if="periodCheck.filed_periods_affected > 0"
          class="rounded border border-[#f3dca8] bg-[#fef8ec] px-3 py-2 text-[#8a6d3b]"
          role="note"
        >
          Note: {{ periodCheck.filed_periods_affected }} previously filed
          {{ periodCheck.filed_periods_affected === 1 ? 'period' : 'periods' }} may no longer appear
          in your VAT return list, because the period dates will change.
        </div>

        <div
          v-if="reconcileError"
          class="rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-[#c0392b]"
          role="alert"
        >
          {{ reconcileError }}
        </div>
      </div>

      <template #footer>
        <button
          type="button"
          class="mr-3 font-semibold text-fa-green hover:underline disabled:opacity-50"
          :disabled="reconcileBusy"
          @click="rejectReconcile"
        >
          Cancel connection
        </button>
        <Button label="Adjust to match HMRC" :loading="reconcileBusy" @click="acceptReconcile" />
      </template>
    </Dialog>
  </AppLayout>
</template>
