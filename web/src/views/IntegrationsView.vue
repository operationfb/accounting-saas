<script setup lang="ts">
// FreeAgent integration settings — the owner/admin screen to CONNECT this
// organisation to FreeAgent and DISCONNECT it. The OAuth app credentials
// (client id / secret) are configured GLOBALLY by the operator in the DB
// (provider_credentials), so there is NO credential entry/edit here — the admin
// only links/unlinks their own org.
//
// MUST live at /settings/integrations: the backend OAuth callback redirects the
// browser back to exactly that path with ?freeagent=connected |
// ?freeagent=error&reason=… (integration_service.go), read on mount for a banner.
//
// States from { has_credentials (global), connected (this org), connected_at }:
//   - !has_credentials → FreeAgent not set up by the operator yet (info only)
//   - has_credentials && !connected → "Connect to FreeAgent"
//   - connected → "Connected since …" + Disconnect
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
} from '@/services/integrations.service'
import type { FreeAgentStatus } from '@/types/integration'
import { formatDateTime } from '@/lib/format'
import type { ApiError } from '@/lib/api'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

// Managing the connection is owner/admin only (the backend GET/DELETE/connect all
// require it). For everyone else we show a note and never call the API.
const canManage = computed(() => auth.isOrgAdmin)

// Friendly text for each backend OAuth `reason` (integration_service.go
// callbackErrorURL) — shown on the error banner after a failed connect.
const REASON_MESSAGES: Record<string, string> = {
  invalid_state: 'The connection link was invalid or expired. Please try connecting again.',
  missing_code: 'FreeAgent didn’t return an authorisation code. Please try again.',
  not_configured: 'FreeAgent isn’t set up yet. Please contact your administrator.',
  exchange_failed: 'FreeAgent rejected the connection. Please try again, or contact your administrator.',
  internal: 'Something went wrong completing the connection. Please try again.',
}

// --- state ---
const loading = ref(true)
const loadError = ref('')
const status = ref<FreeAgentStatus | null>(null)
const banner = reactive<{ type: 'success' | 'error' | ''; message: string }>({ type: '', message: '' })

const connecting = ref(false) // building the authorize URL + navigating away
const disconnecting = ref(false)
const actionError = ref('')

// Read the OAuth-callback query params once, show a banner, then strip them so a
// refresh doesn't re-show it.
function consumeCallbackQuery() {
  const fa = route.query.freeagent
  if (fa === 'connected') {
    banner.type = 'success'
    banner.message = 'Connected to FreeAgent.'
  } else if (fa === 'error') {
    const reason = typeof route.query.reason === 'string' ? route.query.reason : ''
    banner.type = 'error'
    banner.message = REASON_MESSAGES[reason] ?? 'Could not connect to FreeAgent. Please try again.'
  } else {
    return
  }
  void router.replace({ query: {} })
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    status.value = await getFreeAgentStatus()
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load your FreeAgent settings.'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  consumeCallbackQuery()
  if (canManage.value) load()
  else loading.value = false
})

// --- connect (OAuth) ---
async function connect() {
  if (connecting.value) return
  actionError.value = ''
  connecting.value = true
  try {
    const url = await getFreeAgentConnectUrl()
    // Full-page navigation in the SAME tab so a sessionStorage session survives the
    // round-trip; FreeAgent redirects back to /settings/integrations. We're leaving
    // the page, so there's no finally-reset of `connecting`.
    window.location.assign(url)
  } catch (err) {
    actionError.value = (err as ApiError)?.message ?? 'Could not start the FreeAgent connection.'
    connecting.value = false
  }
}

// --- disconnect ---
async function disconnect() {
  if (disconnecting.value) return
  actionError.value = ''
  banner.type = ''
  disconnecting.value = true
  try {
    await disconnectFreeAgent()
    await load()
    banner.type = 'success'
    banner.message = 'Disconnected from FreeAgent. You can reconnect anytime.'
  } catch (err) {
    actionError.value = (err as ApiError)?.message ?? 'Could not disconnect.'
  } finally {
    disconnecting.value = false
  }
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">Integrations</h1>

    <!-- Non-admin: no access to integration settings. -->
    <FaCard v-if="!canManage" title="FreeAgent">
      <div
        class="rounded border border-fa-border bg-fa-card-header px-3 py-2 text-sm text-fa-muted"
        role="note"
      >
        Only owners and admins can manage integrations.
      </div>
    </FaCard>

    <template v-else>
      <!-- Banner: OAuth callback result or disconnect feedback. -->
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

      <!-- Loading -->
      <FaCard v-if="loading" title="FreeAgent">
        <div class="py-10 text-center text-fa-muted">
          <i class="pi pi-spin pi-spinner mr-2" />Loading…
        </div>
      </FaCard>

      <!-- Load error -->
      <FaCard v-else-if="loadError" title="FreeAgent">
        <div class="py-8 text-center">
          <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
          <Button label="Try again" severity="secondary" outlined @click="load" />
        </div>
      </FaCard>

      <FaCard v-else-if="status" title="FreeAgent">
        <p class="mb-4 text-sm text-fa-muted">
          Connect this organisation to FreeAgent so approved expenses are pushed automatically. If a
          push fails you can re-push it from the expense.
        </p>

        <div
          v-if="actionError"
          class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
          role="alert"
        >
          {{ actionError }}
        </div>

        <!-- CONNECTED -->
        <div v-if="status.connected" class="space-y-3">
          <div class="flex flex-wrap items-center gap-2 text-sm">
            <span
              class="inline-block rounded-full border border-[#cfe9c7] bg-[#eaf7e6] px-2.5 py-0.5 text-xs font-semibold text-[#3f8038]"
              >Connected</span
            >
            <span v-if="status.connected_at" class="text-fa-muted"
              >since {{ formatDateTime(status.connected_at) }}</span
            >
          </div>
          <Button
            label="Disconnect"
            severity="danger"
            outlined
            :loading="disconnecting"
            @click="disconnect"
          />
        </div>

        <!-- CONFIGURED, NOT CONNECTED -->
        <div v-else-if="status.has_credentials" class="space-y-3">
          <div class="flex flex-wrap items-center gap-2 text-sm">
            <span
              class="inline-block rounded-full border border-[#dde2e8] bg-[#eef1f4] px-2.5 py-0.5 text-xs font-semibold text-[#5b6772]"
              >Not connected</span
            >
          </div>
          <Button
            label="Connect to FreeAgent"
            icon="pi pi-link"
            :loading="connecting"
            @click="connect"
          />
        </div>

        <!-- NOT CONFIGURED (no global app credentials in the DB yet) -->
        <div
          v-else
          class="rounded border border-fa-border bg-fa-card-header px-3 py-2 text-sm text-fa-muted"
          role="note"
        >
          FreeAgent isn’t set up yet. Once your administrator has configured it, you’ll be able to
          connect this organisation here.
        </div>
      </FaCard>
    </template>
  </AppLayout>
</template>
