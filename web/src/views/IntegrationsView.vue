<script setup lang="ts">
// FreeAgent integration settings — the owner/admin screen to (1) save the OAuth app
// credentials, (2) run the one-time connect, and (3) disconnect. It MUST live at
// /settings/integrations: the backend OAuth callback redirects the browser back to
// exactly that path with ?freeagent=connected | ?freeagent=error&reason=…
// (integration_service.go), which we read on mount to show a banner.
//
// Three states from { has_credentials, connected }:
//   - !has_credentials              → credentials form (+ setup guidance)
//   - has_credentials && !connected → "Connect to FreeAgent" (+ edit credentials)
//   - connected                     → "Connected since …" + Disconnect
import { ref, reactive, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import InputText from 'primevue/inputtext'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { useAuthStore } from '@/stores/auth'
import {
  getFreeAgentStatus,
  saveFreeAgentCredentials,
  disconnectFreeAgent,
  getFreeAgentConnectUrl,
} from '@/services/integrations.service'
import type { FreeAgentStatus } from '@/types/integration'
import { formatDateTime } from '@/lib/format'
import type { ApiError } from '@/lib/api'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

// Managing an integration is owner/admin only (the backend GET/PUT/DELETE all
// require it). For everyone else we show a note and never call the API.
const canManage = computed(() => auth.isOrgAdmin)

// Friendly text for each backend OAuth `reason` (integration_service.go
// callbackErrorURL) — shown on the error banner after a failed connect.
const REASON_MESSAGES: Record<string, string> = {
  invalid_state: 'The connection link was invalid or expired. Please try connecting again.',
  missing_code: 'FreeAgent didn’t return an authorisation code. Please try again.',
  not_configured: 'Save your FreeAgent credentials before connecting.',
  exchange_failed:
    'FreeAgent rejected the connection. Check your Client ID and Secret, then try again.',
  internal: 'Something went wrong completing the connection. Please try again.',
}

// --- state ---
const loading = ref(true)
const loadError = ref('')
const status = ref<FreeAgentStatus | null>(null)
const banner = reactive<{ type: 'success' | 'error' | ''; message: string }>({ type: '', message: '' })

const form = reactive({ clientId: '', clientSecret: '' })
const errors = reactive<Record<string, string>>({})
const editingCredentials = ref(false)

const submitting = ref(false) // saving credentials
const connecting = ref(false) // building the authorize URL + navigating away
const disconnecting = ref(false)
const actionError = ref('')

// Render the credentials form when nothing is saved yet, or the user chose to edit.
const showForm = computed(
  () => !!status.value && (!status.value.has_credentials || editingCredentials.value),
)

// The path the admin appends to their backend URL when registering the FreeAgent
// app's redirect URI. We can't know API_PUBLIC_URL from the SPA, so show the path.
const CALLBACK_PATH = '/api/v1/freeagent/callback'

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

// --- save credentials ---
function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (form.clientId.trim() === '') errors.clientId = 'Enter your FreeAgent Client ID.'
  if (form.clientSecret.trim() === '') errors.clientSecret = 'Enter your FreeAgent Client Secret.'
  return Object.keys(errors).length === 0
}

async function saveCredentials() {
  if (submitting.value) return
  actionError.value = ''
  banner.type = ''
  if (!validate()) return
  submitting.value = true
  try {
    status.value = await saveFreeAgentCredentials({
      client_id: form.clientId.trim(),
      client_secret: form.clientSecret.trim(),
    })
    // Don't keep the secret in memory longer than needed, and leave edit mode.
    form.clientSecret = ''
    editingCredentials.value = false
    banner.type = 'success'
    banner.message = 'Credentials saved. Now connect to FreeAgent.'
  } catch (err) {
    actionError.value = (err as ApiError)?.message ?? 'Could not save your credentials.'
  } finally {
    submitting.value = false
  }
}

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
    banner.message =
      'Disconnected from FreeAgent. Your credentials are kept, so you can reconnect anytime.'
  } catch (err) {
    actionError.value = (err as ApiError)?.message ?? 'Could not disconnect.'
  } finally {
    disconnecting.value = false
  }
}

// Reveal the credentials form from the connect / connected states.
function showCredentialsForm() {
  form.clientId = ''
  form.clientSecret = ''
  for (const k of Object.keys(errors)) delete errors[k]
  editingCredentials.value = true
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
      <!-- Banner: OAuth callback result, save, or disconnect feedback. -->
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

      <template v-else-if="status">
        <FaCard title="FreeAgent">
          <p class="mb-4 text-sm text-fa-muted">
            Push approved expenses to your FreeAgent account automatically.
          </p>

          <div
            v-if="actionError"
            class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
            role="alert"
          >
            {{ actionError }}
          </div>

          <!-- CONNECTED -->
          <div v-if="status.connected && !showForm" class="space-y-3">
            <div class="flex flex-wrap items-center gap-2 text-sm">
              <span
                class="inline-block rounded-full border border-[#cfe9c7] bg-[#eaf7e6] px-2.5 py-0.5 text-xs font-semibold text-[#3f8038]"
                >Connected</span
              >
              <span v-if="status.connected_at" class="text-fa-muted"
                >since {{ formatDateTime(status.connected_at) }}</span
              >
            </div>
            <div class="flex flex-wrap items-center gap-3">
              <Button
                label="Disconnect"
                severity="danger"
                outlined
                :loading="disconnecting"
                @click="disconnect"
              />
              <button
                type="button"
                class="font-semibold text-fa-blue hover:underline"
                @click="showCredentialsForm"
              >
                Edit credentials
              </button>
            </div>
          </div>

          <!-- SAVED, NOT CONNECTED -->
          <div v-else-if="status.has_credentials && !showForm" class="space-y-3">
            <div class="flex flex-wrap items-center gap-2 text-sm">
              <span
                class="inline-block rounded-full border border-[#dde2e8] bg-[#eef1f4] px-2.5 py-0.5 text-xs font-semibold text-[#5b6772]"
                >Not connected</span
              >
              <span class="text-fa-muted">Credentials saved.</span>
            </div>
            <div class="flex flex-wrap items-center gap-3">
              <Button
                label="Connect to FreeAgent"
                icon="pi pi-link"
                :loading="connecting"
                @click="connect"
              />
              <button
                type="button"
                class="font-semibold text-fa-blue hover:underline"
                @click="showCredentialsForm"
              >
                Edit credentials
              </button>
            </div>
          </div>

          <!-- CREDENTIALS FORM -->
          <div v-else>
            <FormRow label="Client ID" label-for="fa-client-id" required>
              <InputText
                id="fa-client-id"
                v-model="form.clientId"
                class="w-full max-w-md"
                autocomplete="off"
                :invalid="!!errors.clientId"
              />
              <p v-if="errors.clientId" class="text-xs text-[#c0392b]">{{ errors.clientId }}</p>
            </FormRow>
            <FormRow label="Client Secret" label-for="fa-client-secret" required>
              <InputText
                id="fa-client-secret"
                v-model="form.clientSecret"
                type="password"
                class="w-full max-w-md"
                autocomplete="off"
                :invalid="!!errors.clientSecret"
              />
              <p v-if="errors.clientSecret" class="text-xs text-[#c0392b]">
                {{ errors.clientSecret }}
              </p>
            </FormRow>
            <div class="flex items-center gap-3 pt-2">
              <Button label="Save credentials" :loading="submitting" @click="saveCredentials" />
              <button
                v-if="status.has_credentials"
                type="button"
                class="font-semibold text-fa-blue hover:underline"
                @click="editingCredentials = false"
              >
                Cancel
              </button>
            </div>
          </div>
        </FaCard>

        <!-- Setup guidance -->
        <FaCard title="Setup">
          <ol class="list-decimal space-y-2 pl-5 text-sm text-fa-text">
            <li>In your FreeAgent developer dashboard, create an OAuth app.</li>
            <li>
              Set its <span class="font-semibold">OAuth redirect URI</span> to your backend URL
              followed by
              <code class="rounded bg-[#eef1f4] px-1 py-0.5 text-[13px]">{{ CALLBACK_PATH }}</code
              >.
            </li>
            <li>
              Copy the app's <span class="font-semibold">Client ID</span> and
              <span class="font-semibold">Client Secret</span> into the fields above and save.
            </li>
            <li>Click <span class="font-semibold">Connect to FreeAgent</span> and approve access.</li>
          </ol>
          <p class="mt-3 text-xs text-fa-muted">
            Once connected, approved expenses are pushed automatically. If a push fails you can
            re-push it from the expense.
          </p>
        </FaCard>
      </template>
    </template>
  </AppLayout>
</template>
