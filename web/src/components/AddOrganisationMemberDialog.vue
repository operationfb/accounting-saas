<script setup lang="ts">
// "Add users" modal (god view) — attach an EXISTING platform user to an org.
// Self-contained: loads the platform user list, collects a user + role, calls the
// admin API, and emits `changed` with the refreshed org detail. Superuser only.
import { reactive, ref, watch, computed } from 'vue'
import Dialog from 'primevue/dialog'
import Select from 'primevue/select'
import Button from 'primevue/button'
import FormRow from '@/components/FormRow.vue'
import { listAllUsers, addAdminOrganisationMember } from '@/services/admin.service'
import type { AdminOrganisationDetail, AdminUser } from '@/types/admin'
import type { ApiError } from '@/lib/api'

const props = defineProps<{ visible: boolean; orgId: string; defaultRole: string }>()
const emit = defineEmits<{
  'update:visible': [value: boolean]
  changed: [detail: AdminOrganisationDetail]
}>()

// The five membership roles (label ≠ value for 'member', matching UsersListView).
const roleOptions = [
  { label: 'Owner', value: 'owner' },
  { label: 'Admin', value: 'admin' },
  { label: 'Employee', value: 'member' },
  { label: 'Accountant', value: 'accountant' },
  { label: 'Read only', value: 'read_only' },
]

const users = ref<AdminUser[]>([])
const userOptions = computed(() =>
  users.value.map((u) => ({
    label: `${`${u.first_name} ${u.last_name}`.trim() || u.email} — ${u.email}`,
    value: u.id,
  })),
)

const form = reactive({ userId: '', role: props.defaultRole })
const errors = reactive<Record<string, string>>({})
const submitting = ref(false)
const formError = ref('')

watch(
  () => props.visible,
  async (open) => {
    if (!open) return
    form.userId = ''
    form.role = props.defaultRole
    for (const k of Object.keys(errors)) delete errors[k]
    formError.value = ''
    if (users.value.length === 0) {
      users.value = await listAllUsers().catch(() => [])
    }
  },
)

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (!form.userId) errors.userId = 'Pick a user.'
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
    const detail = await addAdminOrganisationMember(props.orgId, { user_id: form.userId, role: form.role })
    emit('changed', detail)
    close()
  } catch (err) {
    // 409 (already a member), 404 (user gone), etc.
    formError.value = (err as ApiError)?.message ?? 'Could not add the user.'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Dialog
    :visible="visible"
    modal
    header="Add user to organisation"
    :style="{ width: '38rem' }"
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

    <FormRow label="User" label-for="add-member-user" required>
      <Select
        id="add-member-user"
        v-model="form.userId"
        :options="userOptions"
        option-label="label"
        option-value="value"
        filter
        filter-placeholder="Search users"
        placeholder="Select a user"
        class="w-full"
        :invalid="!!errors.userId"
      />
      <p v-if="errors.userId" class="text-xs text-[#c0392b]">{{ errors.userId }}</p>
    </FormRow>

    <FormRow label="Role" label-for="add-member-role" required>
      <Select
        id="add-member-role"
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
      <Button label="Add user" :loading="submitting" @click="submit" />
    </template>
  </Dialog>
</template>
