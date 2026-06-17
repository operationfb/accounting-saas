<script setup lang="ts">
// My Details — the signed-in user's own profile screen, modelled on FreeAgent's
// "My Details". Like Company Details it is a SINGLETON (GET/PUT /api/v1/profile,
// the user comes from the token), so there is no create mode and no id in the URL.
//
// Unlike Company Details there is NO role gate: every user edits their OWN
// profile, so the form is always editable (the backend is self-scoped from the
// token — you can only ever change yourself).
//
// Two cards:
//   1. User details — read-only login email + editable first/last name.
//   2. Email receipts — the per-user Mailgun forwarding address (read-only, with
//      a copy button). Shown only when the email-to-expense channel is enabled.
import { ref, reactive, onMounted } from 'vue'
import InputText from 'primevue/inputtext'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { useAuthStore } from '@/stores/auth'
import { getProfile, updateProfile, getInboxAddress } from '@/services/user.service'
import type { User } from '@/types/auth'
import type { ApiError } from '@/lib/api'

const auth = useAuthStore()

// --- form state (only the editable fields live here) ---
const form = reactive({ firstName: '', lastName: '' })
// The login email is read-only. Seed from the cached session so it shows
// instantly, then overwrite with the authoritative value from the profile load.
const email = ref(auth.user?.email ?? '')

// --- load state ---
const loading = ref(true) // spinner until the profile is fetched
const loadError = ref('')
const loaded = ref<User | null>(null) // last saved record (for Cancel)

// Copy a fetched/updated user into the reactive form.
function hydrate(u: User) {
  form.firstName = u.first_name
  form.lastName = u.last_name
  email.value = u.email
}

// --- email-receipts inbox (Mailgun) ---
const inboxEnabled = ref(false)
const inboxAddress = ref('')
const copied = ref(false)

async function loadInbox() {
  try {
    const res = await getInboxAddress()
    inboxEnabled.value = res.enabled
    inboxAddress.value = res.address
  } catch {
    // Non-fatal: if the inbox lookup fails we simply don't show the card.
    inboxEnabled.value = false
  }
}

async function copyInbox() {
  try {
    await navigator.clipboard.writeText(inboxAddress.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    // Clipboard can be blocked (e.g. insecure context); ignore — the address is
    // visible and selectable anyway.
  }
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const u = await getProfile()
    loaded.value = u
    hydrate(u)
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load your details.'
  } finally {
    loading.value = false
  }
  // The inbox address loads independently — its absence must not block the page.
  void loadInbox()
}

onMounted(load)

// --- validation ---
const errors = reactive<Record<string, string>>({})

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (form.firstName.trim() === '') errors.firstName = 'Enter your first name.'
  if (form.lastName.trim() === '') errors.lastName = 'Enter your last name.'
  return Object.keys(errors).length === 0
}

// --- submit ---
const submitting = ref(false)
const formError = ref('')
const successMessage = ref('')

async function submit() {
  if (submitting.value) return
  formError.value = ''
  successMessage.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    const updated = await updateProfile({
      first_name: form.firstName.trim(),
      last_name: form.lastName.trim(),
    })
    loaded.value = updated
    hydrate(updated)
    // Keep the top-bar dropdown name in sync after a rename.
    auth.patchUser({ first_name: updated.first_name, last_name: updated.last_name })
    successMessage.value = 'Your details have been saved.'
  } catch (err) {
    // 401 is handled by apiFetch. 400/422 land here.
    formError.value = (err as ApiError)?.message ?? 'Could not save your changes. Please try again.'
  } finally {
    submitting.value = false
  }
}

// Cancel discards edits by re-applying the last saved record (this is a settings
// singleton, so there's no list to navigate back to).
function cancel() {
  if (loaded.value) hydrate(loaded.value)
  for (const k of Object.keys(errors)) delete errors[k]
  formError.value = ''
  successMessage.value = ''
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">My Details</h1>

    <!-- Loading -->
    <FaCard v-if="loading" title="User details">
      <div class="py-10 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>
    </FaCard>

    <!-- Load error -->
    <FaCard v-else-if="loadError" title="User details">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button label="Try again" severity="secondary" outlined @click="load" />
      </div>
    </FaCard>

    <!-- The form (loaded ok) -->
    <template v-else>
      <div
        v-if="formError"
        class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
        role="alert"
      >
        {{ formError }}
      </div>
      <div
        v-if="successMessage"
        class="mb-4 rounded border border-[#cfe9c7] bg-[#eaf7e6] px-3 py-2 text-sm text-[#3f8038]"
        role="status"
      >
        {{ successMessage }}
      </div>

      <!-- 1. User details -->
      <FaCard title="User details" note="Required fields *">
        <FormRow label="Login / email" label-for="email">
          <InputText id="email" :value="email" class="w-full max-w-md" disabled />
          <p class="text-xs text-fa-muted">This is your login email and can't be changed here.</p>
        </FormRow>
        <FormRow label="First name" label-for="first-name" required>
          <InputText
            id="first-name"
            v-model="form.firstName"
            class="w-72"
            :invalid="!!errors.firstName"
          />
          <p v-if="errors.firstName" class="text-xs text-[#c0392b]">{{ errors.firstName }}</p>
        </FormRow>
        <FormRow label="Last name" label-for="last-name" required>
          <InputText
            id="last-name"
            v-model="form.lastName"
            class="w-72"
            :invalid="!!errors.lastName"
          />
          <p v-if="errors.lastName" class="text-xs text-[#c0392b]">{{ errors.lastName }}</p>
        </FormRow>
      </FaCard>

      <!-- 2. Email receipts (only when the inbox channel is enabled) -->
      <FaCard v-if="inboxEnabled" title="Email receipts">
        <p class="mb-2 text-sm text-fa-muted">
          Forward receipts to this address and they'll become draft expenses automatically.
        </p>
        <FormRow label="Your inbox address" label-for="inbox-address">
          <div class="flex w-full max-w-md items-center gap-2">
            <InputText
              id="inbox-address"
              :value="inboxAddress"
              class="min-w-0 flex-1"
              readonly
            />
            <Button
              :label="copied ? 'Copied' : 'Copy'"
              :icon="copied ? 'pi pi-check' : 'pi pi-copy'"
              severity="secondary"
              outlined
              @click="copyInbox"
            />
          </div>
        </FormRow>
      </FaCard>

      <div class="flex items-center gap-3 py-2 pb-6">
        <Button label="Save changes" :loading="submitting" @click="submit" />
        <button type="button" class="font-semibold text-fa-blue hover:underline" @click="cancel">
          Cancel
        </button>
      </div>
    </template>
  </AppLayout>
</template>
