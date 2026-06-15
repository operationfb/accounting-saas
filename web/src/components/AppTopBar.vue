<script setup lang="ts">
// Top navigation bar (the navy strip), mirroring FA's app chrome.
// The company button on the right opens an account dropdown (Change Password,
// Logout).
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import Menu from 'primevue/menu'
import type { MenuItem } from 'primevue/menuitem'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()

// Static nav items mirroring FreeAgent's top navigation. "My Money" is shown as
// the active section because expenses live under it.
const navItems = [
  { label: 'Overview', caret: false, active: false },
  { label: 'Contacts', caret: false, active: false },
  { label: 'Work', caret: true, active: false },
  { label: 'Bills', caret: false, active: false },
  { label: 'My Money', caret: true, active: true },
  { label: 'Banking', caret: true, active: false },
  { label: 'Taxes', caret: true, active: false },
  { label: 'Accounting', caret: true, active: false },
]

// Account / organisation dropdown. ref holds the PrimeVue popup Menu instance.
const accountMenu = ref()
const accountItems = ref<MenuItem[]>([
  { label: 'Change Password', icon: 'pi pi-key', command: () => changePassword() },
  { label: 'Logout', icon: 'pi pi-sign-out', command: () => logout() },
])

function toggleAccountMenu(event: Event) {
  accountMenu.value?.toggle(event)
}

function logout() {
  auth.logout()
  router.push('/login')
}

// "Change Password" enters the password-reset flow. That flow is unauthenticated
// (the reset page is reached via an emailed token), so we log the user out first
// and then send them to /forgot to request a reset link.
function changePassword() {
  auth.logout()
  router.push('/forgot')
}
</script>

<template>
  <header class="bg-fa-nav text-white">
    <nav class="mx-auto flex h-[46px] max-w-[1200px] items-stretch justify-between px-4">
      <ul class="flex items-stretch gap-0.5">
        <li
          v-for="item in navItems"
          :key="item.label"
          class="flex cursor-pointer items-center gap-1 whitespace-nowrap px-3 text-sm font-medium hover:bg-fa-nav-active"
          :class="{ 'bg-fa-nav-active': item.active }"
        >
          {{ item.label }}
          <i v-if="item.caret" class="pi pi-angle-down text-[11px] opacity-[0.85]" />
        </li>
      </ul>

      <div class="flex items-center gap-1.5">
        <button
          type="button"
          class="inline-flex h-[34px] w-[34px] items-center justify-center rounded text-white hover:bg-fa-nav-active"
          aria-label="Search"
        >
          <i class="pi pi-search" />
        </button>
        <button
          type="button"
          class="relative inline-flex h-[34px] w-[34px] items-center justify-center rounded text-white hover:bg-fa-nav-active"
          aria-label="Notifications"
        >
          <i class="pi pi-bell" />
          <span class="absolute right-[7px] top-[7px] h-2 w-2 rounded-full bg-fa-green" />
        </button>

        <!-- Company button → account dropdown (Logout) -->
        <button
          id="account-menu-button"
          type="button"
          class="inline-flex h-[34px] items-center gap-1.5 rounded px-2 text-[13px] font-semibold uppercase text-white hover:bg-fa-nav-active"
          aria-haspopup="true"
          aria-controls="account-menu"
          @click="toggleAccountMenu"
        >
          {{ auth.organisation?.name || 'Organisation' }}
          <i class="pi pi-angle-down text-[11px] opacity-[0.85]" />
        </button>
        <Menu id="account-menu" ref="accountMenu" :model="accountItems" :popup="true">
          <template #start>
            <div v-if="auth.user" class="border-b border-fa-border px-3 py-2">
              <div class="text-sm font-semibold text-fa-text">
                {{ auth.user.first_name }} {{ auth.user.last_name }}
              </div>
              <div class="text-xs text-fa-muted">{{ auth.user.email }}</div>
            </div>
          </template>
        </Menu>
      </div>
    </nav>
  </header>
</template>
