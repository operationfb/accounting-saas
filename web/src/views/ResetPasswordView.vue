<script setup lang="ts">
// Reset-password ("choose a new password") screen — standalone full-page,
// modelled on FreeAgent's reset page and matching our Login/Forgot views.
//
// Reached from the password-reset email link, which carries a one-time token as
// a URL PATH segment (/reset-password/:token). Wired to POST
// /api/v1/auth/reset-password/:token; on success we confirm and send the user to
// log in with their new password.
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import InputText from 'primevue/inputtext'
import Button from 'primevue/button'
import { resetPassword } from '@/services/auth.service'
import type { ApiError } from '@/lib/api'
// Same imported-asset approach as the other auth pages (Vite fingerprints it).
import kontalaLogo from '@/assets/kontala-logo.png'

const route = useRoute()
const router = useRouter()

// The raw reset code from the email link. It only ever travels in the URL; the
// backend stores just a hash of it.
const token = String(route.params.token ?? '')

const password = ref('')
const showPassword = ref(false)
const pending = ref(false)
const errorMessage = ref('')
// On success we swap the form for a confirmation + a link to log in.
const done = ref(false)

// Mirror the backend's binding:"min=8" so a too-short password fails fast with a
// friendly message instead of surfacing the server's raw validation error.
const MIN_PASSWORD_LENGTH = 8

async function onSubmit() {
  if (pending.value) return
  errorMessage.value = ''
  if (password.value.length < MIN_PASSWORD_LENGTH) {
    errorMessage.value = `Please choose a password of at least ${MIN_PASSWORD_LENGTH} characters.`
    return
  }
  pending.value = true
  try {
    await resetPassword(token, password.value)
    done.value = true
  } catch (err) {
    // e.g. "invalid or expired reset link" for a stale/used token.
    errorMessage.value = (err as ApiError)?.message ?? 'Something went wrong. Please try again.'
  } finally {
    pending.value = false
  }
}
</script>

<template>
  <div class="flex min-h-screen flex-col items-center gap-[22px] bg-fa-bg px-4 pb-10 pt-14">
    <img :src="kontalaLogo" alt="Kontala" class="h-10 w-auto select-none" />

    <div class="w-full max-w-[380px] rounded-md bg-white p-7 shadow-[0_1px_3px_rgba(20,40,80,0.12)]">
      <!-- Success: confirm and send them to log in with the new password. -->
      <template v-if="done">
        <h1 class="mb-2 text-xl font-bold">Password updated</h1>
        <p class="mb-[22px] text-sm leading-relaxed">
          Your password has been changed. You can now log in with your new password.
        </p>
        <Button label="Log in" class="w-full font-semibold" @click="router.push('/login')" />
      </template>

      <!-- Default: the choose-a-password form. -->
      <template v-else>
        <h1 class="mb-2 text-xl font-bold">Choose a new password</h1>
        <p class="mb-[22px] text-sm leading-relaxed">
          Try to avoid using a password from another site or something too obvious,
          like your pet's name.
          <a href="#" class="text-fa-blue hover:underline"
            >Find out more about setting a secure password.</a
          >
        </p>

        <div
          v-if="errorMessage"
          class="mb-4 flex items-start gap-2.5 rounded-md border-l-4 border-[#c0392b] bg-[#fdecec] px-3 py-2.5 text-sm text-[#3c4043]"
          role="alert"
        >
          <i class="pi pi-exclamation-triangle mt-0.5 text-[#c0392b]" aria-hidden="true" />
          <span>{{ errorMessage }}</span>
        </div>

        <form class="flex flex-col" @submit.prevent="onSubmit">
          <label class="mb-1.5 block text-sm" for="password">Create a password</label>
          <div class="relative">
            <InputText
              id="password"
              v-model="password"
              :type="showPassword ? 'text' : 'password'"
              class="w-full"
              autocomplete="new-password"
            />
            <!-- Show/Hide is pure presentation (toggles the input type), like LoginView. -->
            <button
              type="button"
              class="absolute right-0 top-0 h-full border-l border-fa-input-border px-3.5 text-sm font-semibold text-fa-blue"
              @click="showPassword = !showPassword"
            >
              {{ showPassword ? 'Hide' : 'Show' }}
            </button>
          </div>

          <Button
            type="submit"
            label="Save my new password"
            :loading="pending"
            class="mt-5 w-full font-semibold"
          />
        </form>
      </template>
    </div>
  </div>
</template>
