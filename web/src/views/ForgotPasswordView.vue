<script setup lang="ts">
// Forgot-password screen — standalone full-page, modelled on FreeAgent's /forgot
// page and matching our LoginView (same logo, card, background).
//
// Wired to POST /api/v1/auth/forgot-password. That endpoint ALWAYS returns 200
// with a generic message (it never reveals whether the address is registered),
// so on success we show a neutral "check your inbox" confirmation.
import { ref } from 'vue'
import InputText from 'primevue/inputtext'
import Button from 'primevue/button'
import { forgotPassword } from '@/services/auth.service'
import type { ApiError } from '@/lib/api'
// Same imported-asset approach as the other auth pages (Vite fingerprints it).
import kontalaLogo from '@/assets/kontala-logo.png'

const email = ref('')
const pending = ref(false)
const errorMessage = ref('')
// On success we swap the form for the generic confirmation below.
const sent = ref(false)

async function onSubmit() {
  if (pending.value) return
  errorMessage.value = ''
  // Light client-side guard so an empty field doesn't bounce off the backend's
  // 400 with a raw binding message.
  if (!email.value.trim()) {
    errorMessage.value = 'Please enter your email address.'
    return
  }
  pending.value = true
  try {
    await forgotPassword(email.value.trim())
    sent.value = true
  } catch (err) {
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
      <h1 class="mb-2 text-xl font-bold">Forgotten your password?</h1>

      <!-- Success: neutral confirmation (the backend never confirms the address). -->
      <p v-if="sent" class="text-sm leading-relaxed">
        If <strong>{{ email.trim() }}</strong> is registered, we've sent a link to
        reset your password. Please check your inbox.
      </p>

      <!-- Default: the request form. -->
      <template v-else>
        <p class="mb-[22px] text-sm text-fa-muted">
          No problem. We'll send you instructions on how to reset it.
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
          <label class="mb-1.5 block text-sm" for="email">Email address</label>
          <InputText
            id="email"
            v-model="email"
            type="email"
            class="w-full"
            autocomplete="email"
            placeholder="Enter your email address"
          />

          <Button
            type="submit"
            label="Reset password"
            :loading="pending"
            class="mt-5 w-full font-semibold"
          />
        </form>
      </template>
    </div>

    <!-- Navigation back to login. -->
    <RouterLink to="/login" class="text-sm font-semibold text-fa-blue hover:underline">
      ← Wait, I remembered it!
    </RouterLink>
  </div>
</template>
