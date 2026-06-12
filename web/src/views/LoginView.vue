<script setup lang="ts">
// Login screen — standalone full-page (no app chrome), modelled on FA's login.
// STATIC: nothing is wired up. The local refs only exist so the PrimeVue inputs
// render; there is no submit/auth logic yet.
import { ref } from 'vue'
import InputText from 'primevue/inputtext'
import Checkbox from 'primevue/checkbox'
import Button from 'primevue/button'

const email = ref('')
const password = ref('')
const keepLoggedIn = ref(false)
</script>

<template>
  <div class="flex min-h-screen flex-col items-center gap-[22px] bg-fa-bg px-4 pb-10 pt-14">
    <div class="text-[34px] font-extrabold italic tracking-[-0.5px] text-fa-blue">Accounting</div>

    <div class="w-full max-w-[380px] rounded-md bg-white p-7 shadow-[0_1px_3px_rgba(20,40,80,0.12)]">
      <h1 class="mb-[22px] text-xl font-bold">Ready to take care of business?</h1>

      <form class="flex flex-col" @submit.prevent>
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
              type="password"
              class="w-full"
              autocomplete="current-password"
            />
            <button
              type="button"
              class="absolute right-0 top-0 h-full border-l border-fa-input-border px-3.5 text-sm font-semibold text-fa-blue"
            >
              Show
            </button>
          </div>
        </div>

        <div class="mb-5 mt-4 flex items-center gap-2 text-sm">
          <label class="inline-flex cursor-pointer items-center gap-2">
            <Checkbox v-model="keepLoggedIn" binary input-id="keep" />
            <span>Keep me logged in</span>
          </label>
          <span class="text-fa-muted">•</span>
          <a href="#" class="text-fa-blue hover:underline">Reset my password</a>
        </div>

        <Button label="Log in" class="w-full font-semibold" />
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
