<script setup lang="ts">
// New user form — owner/admin adds a user to the organisation.
//   /users/new → create (POST /api/v1/members)
// The admin sets an initial password, so the user is created ACTIVE and can log in
// immediately (no email invite). Modelled on ContactEntryView (FaCard / FormRow /
// inline-error pattern). Role excludes 'owner' (owner is assigned later on the User
// Details screen, behind the backend's owner-only guard). A non-admin who reaches
// this route is redirected — the route, view and API all gate to owner/admin.
import { reactive, ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { useAuthStore } from '@/stores/auth'
import { createMember } from '@/services/members.service'
import type { CreateMemberRequest } from '@/types/member'
import type { ApiError } from '@/lib/api'

const router = useRouter()
const auth = useAuthStore()

// Matches the backend's password binding (min=8) so a too-short value fails fast.
const MIN_PASSWORD_LENGTH = 8
// Simple email shape check (the backend's `email` binding is the final authority).
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

// Role options. NOTE the deliberate label≠value divergence (mirrors UsersListView):
// the `member` access role is shown as "Employee". 'owner' is intentionally absent.
const roleOptions = [
  { label: 'Employee', value: 'member' },
  { label: 'Admin', value: 'admin' },
  { label: 'Accountant', value: 'accountant' },
  { label: 'Read only', value: 'read_only' },
]

const form = reactive({
  email: '',
  firstName: '',
  lastName: '',
  password: '',
  role: 'member' as CreateMemberRequest['role'],
})
const showPassword = ref(false)

const errors = reactive<Record<string, string>>({})
const submitting = ref(false)
const formError = ref('')

// Belt-and-braces: a non-admin shouldn't be here (the API 403s anyway).
onMounted(() => {
  if (!auth.isOrgAdmin) router.replace('/my-details')
})

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]

  if (!form.firstName.trim()) errors.firstName = 'Enter a first name.'
  if (!form.lastName.trim()) errors.lastName = 'Enter a last name.'
  if (!form.email.trim()) {
    errors.email = 'Enter an email address.'
  } else if (!EMAIL_RE.test(form.email.trim())) {
    errors.email = 'Enter a valid email address.'
  }
  if (form.password.length < MIN_PASSWORD_LENGTH) {
    errors.password = `Choose a password of at least ${MIN_PASSWORD_LENGTH} characters.`
  }

  return Object.keys(errors).length === 0
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    await createMember({
      email: form.email.trim(),
      password: form.password,
      first_name: form.firstName.trim(),
      last_name: form.lastName.trim(),
      role: form.role,
    })
    router.push('/users')
  } catch (err) {
    // 401 is handled by apiFetch. 400/403/409/422 land here (e.g. duplicate email).
    formError.value = (err as ApiError)?.message ?? 'Could not add this user. Please try again.'
  } finally {
    submitting.value = false
  }
}

function cancel() {
  router.push('/users')
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">New User</h1>

    <div
      v-if="formError"
      class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
      role="alert"
    >
      {{ formError }}
    </div>

    <FaCard title="User Details" note="Required fields *">
      <FormRow label="First Name" label-for="first-name" required>
        <InputText id="first-name" v-model="form.firstName" class="w-full sm:w-72" :invalid="!!errors.firstName" />
        <p v-if="errors.firstName" class="text-xs text-[#c0392b]">{{ errors.firstName }}</p>
      </FormRow>
      <FormRow label="Last Name" label-for="last-name" required>
        <InputText id="last-name" v-model="form.lastName" class="w-full sm:w-72" :invalid="!!errors.lastName" />
        <p v-if="errors.lastName" class="text-xs text-[#c0392b]">{{ errors.lastName }}</p>
      </FormRow>
      <FormRow label="Email" label-for="email" required>
        <InputText id="email" v-model="form.email" type="email" autocomplete="off" class="w-full max-w-md" :invalid="!!errors.email" />
        <p class="text-xs text-fa-muted">The user logs in with this email address.</p>
        <p v-if="errors.email" class="text-xs text-[#c0392b]">{{ errors.email }}</p>
      </FormRow>
      <FormRow label="Password" label-for="password" required>
        <div class="relative w-full max-w-md">
          <InputText
            id="password"
            v-model="form.password"
            :type="showPassword ? 'text' : 'password'"
            autocomplete="new-password"
            class="w-full"
            :invalid="!!errors.password"
          />
          <button
            type="button"
            class="absolute right-0 top-0 h-full border-l border-fa-input-border px-3.5 text-sm font-semibold text-fa-blue"
            @click="showPassword = !showPassword"
          >
            {{ showPassword ? 'Hide' : 'Show' }}
          </button>
        </div>
        <p class="text-xs text-fa-muted">
          Set an initial password (at least {{ MIN_PASSWORD_LENGTH }} characters) and share it with the user.
        </p>
        <p v-if="errors.password" class="text-xs text-[#c0392b]">{{ errors.password }}</p>
      </FormRow>
      <FormRow label="Role" label-for="role" required>
        <Select
          id="role"
          v-model="form.role"
          :options="roleOptions"
          option-label="label"
          option-value="value"
          class="w-full sm:w-72"
        />
      </FormRow>
    </FaCard>

    <div class="flex items-center gap-3 py-2 pb-6">
      <Button label="Add user" :loading="submitting" @click="submit" />
      <button type="button" class="font-semibold text-fa-green hover:underline" @click="cancel">Cancel</button>
    </div>
  </AppLayout>
</template>
