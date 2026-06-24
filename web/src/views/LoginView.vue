<script setup lang="ts">
// Login screen — standalone full-page (no app chrome), modelled on FA's login.
// Wired to the real POST /api/v1/auth/login via the auth store.
import { ref, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import InputText from 'primevue/inputtext'
import Checkbox from 'primevue/checkbox'
import Button from 'primevue/button'
import { useAuthStore } from '@/stores/auth'
import type { ApiError } from '@/lib/api'
// Imported (not in /public) so Vite fingerprints the file for cache-busting.
import kontalaLogo from '@/assets/kontala-logo-green.png'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()

const email = ref('')
const password = ref('')
const keepLoggedIn = ref(false)
const showPassword = ref(false)

const pending = ref(false)
const errorMessage = ref('')

// The backend (auth_handler.go) returns a plain "invalid email or password"
// string for bad credentials. Show a friendlier wording for that specific case,
// but pass any other message through unchanged — notably the account-lockout
// notice ("account is temporarily locked…"), which we must not hide.
const displayError = computed(() =>
  errorMessage.value === 'invalid email or password'
    ? 'The email and password you entered were incorrect'
    : errorMessage.value,
)

async function onSubmit() {
  if (pending.value) return
  errorMessage.value = ''
  pending.value = true
  try {
    await auth.login(email.value, password.value, keepLoggedIn.value)
    // Return to wherever the guard sent us from, else the list.
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/expenses'
    await router.replace(redirect)
  } catch (err) {
    // ApiError.message carries the backend's "invalid email or password" /
    // "account is temporarily locked…" text.
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
      <h1 class="mb-[22px] text-xl font-bold">Ready to take care of business?</h1>

      <div
        v-if="errorMessage"
        class="mb-4 flex items-start gap-2.5 rounded-md border-l-4 border-[#c0392b] bg-[#fdecec] px-3 py-2.5 text-sm text-[#3c4043]"
        role="alert"
      >
        <!-- Decorative: the text already conveys the error, so hide from a11y tree. -->
        <i class="pi pi-exclamation-triangle mt-0.5 text-[#c0392b]" aria-hidden="true" />
        <span>{{ displayError }}</span>
      </div>

      <form class="flex flex-col" @submit.prevent="onSubmit">
        <div>
          <label class="mb-1.5 block text-sm" for="email">Email address</label>
          <InputText id="email" v-model="email" class="w-full" autocomplete="username" />
        </div>

        <div class="mt-3.5">
          <label class="mb-1.5 block text-sm" for="password">Password</label>
          <div class="relative">
            <InputText
              id="password"
              v-model="password"
              :type="showPassword ? 'text' : 'password'"
              class="w-full"
              autocomplete="current-password"
            />
            <button
              type="button"
              class="absolute right-0 top-0 h-full border-l border-fa-input-border px-3.5 text-sm font-semibold text-fa-blue"
              @click="showPassword = !showPassword"
            >
              {{ showPassword ? 'Hide' : 'Show' }}
            </button>
          </div>
        </div>

        <div class="mb-5 mt-4 flex items-center gap-2 text-sm">
          <label class="inline-flex cursor-pointer items-center gap-2">
            <Checkbox v-model="keepLoggedIn" binary input-id="keep" />
            <span>Keep me logged in</span>
          </label>
          <span class="text-fa-muted">•</span>
          <RouterLink to="/forgot" class="text-fa-blue hover:underline">Reset my password</RouterLink>
        </div>

        <Button type="submit" label="Log in" :loading="pending" class="w-full font-semibold" />
      </form>
    </div>

    <button
      type="button"
      class="inline-flex items-center gap-2.5 rounded border border-fa-border bg-white px-[18px] py-2.5 text-sm font-semibold text-[#3c4043]"
    >
      <span class="font-extrabold text-[#4285f4]">G</span>
      Sign in with Google
    </button>
  </div>
</template>
