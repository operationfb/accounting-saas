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
import type { FreeAgentStatus, HmrcStatus } from '@/types/integration'
import { formatDateTime } from '@/lib/format'
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
  consumeCallbackQuery()
  if (canManage.value) {
    loadHmrc()
    loadFa()
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
  </AppLayout>
</template>
