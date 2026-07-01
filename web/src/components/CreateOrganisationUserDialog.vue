<script setup lang="ts">
// "Create User" modal (god view) — create a NEW platform user and attach them to
// an org. Self-contained: collects email/name/password/role, calls the admin API,
// and emits `changed` with the refreshed org detail. Superuser only. The superuser
// sets an initial password (no email-invite step — that flow stays deferred).
import { reactive, ref, watch } from 'vue'
import Dialog from 'primevue/dialog'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Button from 'primevue/button'
import FormRow from '@/components/FormRow.vue'
import { createAdminOrganisationUser } from '@/services/admin.service'
import type { AdminOrganisationDetail } from '@/types/admin'
import type { ApiError } from '@/lib/api'

const props = defineProps<{ visible: boolean; orgId: string; defaultRole: string }>()
const emit = defineEmits<{
  'update:visible': [value: boolean]
  changed: [detail: AdminOrganisationDetail]
}>()

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
const roleOptions = [
  { label: 'Owner', value: 'owner' },
  { label: 'Admin', value: 'admin' },
  { label: 'Employee', value: 'member' },
  { label: 'Accountant', value: 'accountant' },
  { label: 'Read only', value: 'read_only' },
]

const defaults = () => ({ email: '', firstName: '', lastName: '', password: '', role: props.defaultRole })
const form = reactive(defaults())
const errors = reactive<Record<string, string>>({})
const submitting = ref(false)
const formError = ref('')
const showPassword = ref(false)

watch(
  () => props.visible,
  (open) => {
    if (!open) return
    Object.assign(form, defaults())
    for (const k of Object.keys(errors)) delete errors[k]
    formError.value = ''
  },
)

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (!EMAIL_RE.test(form.email.trim())) errors.email = 'Enter a valid email address.'
  if (form.firstName.trim() === '') errors.firstName = 'Enter a first name.'
  if (form.lastName.trim() === '') errors.lastName = 'Enter a last name.'
  if (form.password.length < 8) errors.password = 'Use at least 8 characters.'
  if (!form.role) errors.role = 'Pick a role.'
  return Object.keys(errors).length === 0
}

function close() {
  emit('update:visible', false)
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    const detail = await createAdminOrganisationUser(props.orgId, {
      email: form.email.trim(),
      password: form.password,
      first_name: form.firstName.trim(),
      last_name: form.lastName.trim(),
      role: form.role,
    })
    emit('changed', detail)
    close()
  } catch (err) {
    // 409 (email exists), etc.
    formError.value = (err as ApiError)?.message ?? 'Could not create the user.'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Dialog
    :visible="visible"
    modal
    header="Create user under organisation"
    :style="{ width: '40rem' }"
    :closable="!submitting"
    @update:visible="(v: boolean) => emit('update:visible', v)"
  >
    <div
      v-if="formError"
      class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
      role="alert"
    >
      {{ formError }}
    </div>

    <p class="mb-4 text-sm text-fa-muted">
      Creates a new platform account and adds it to this organisation. Share the password with the
      user — there is no invite email yet.
    </p>

    <FormRow label="Email" label-for="new-user-email" required>
      <InputText
        id="new-user-email"
        v-model="form.email"
        class="w-full"
        :invalid="!!errors.email"
      />
      <p v-if="errors.email" class="text-xs text-[#c0392b]">{{ errors.email }}</p>
    </FormRow>

    <FormRow label="First name" label-for="new-user-first" required>
      <InputText
        id="new-user-first"
        v-model="form.firstName"
        class="w-full sm:w-72"
        :invalid="!!errors.firstName"
      />
      <p v-if="errors.firstName" class="text-xs text-[#c0392b]">{{ errors.firstName }}</p>
    </FormRow>

    <FormRow label="Last name" label-for="new-user-last" required>
      <InputText
        id="new-user-last"
        v-model="form.lastName"
        class="w-full sm:w-72"
        :invalid="!!errors.lastName"
      />
      <p v-if="errors.lastName" class="text-xs text-[#c0392b]">{{ errors.lastName }}</p>
    </FormRow>

    <FormRow label="Initial password" label-for="new-user-password" required>
      <div class="relative w-full sm:w-72">
        <InputText
          id="new-user-password"
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
      <p v-if="errors.password" class="text-xs text-[#c0392b]">{{ errors.password }}</p>
    </FormRow>

    <FormRow label="Role" label-for="new-user-role" required>
      <Select
        id="new-user-role"
        v-model="form.role"
        :options="roleOptions"
        option-label="label"
        option-value="value"
        class="w-full sm:w-60"
        :invalid="!!errors.role"
      />
    </FormRow>

    <template #footer>
      <Button label="Cancel" severity="secondary" outlined :disabled="submitting" @click="close" />
      <Button label="Create User" :loading="submitting" @click="submit" />
    </template>
  </Dialog>
</template>
