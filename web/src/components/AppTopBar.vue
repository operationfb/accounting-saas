<script setup lang="ts">
// Top navigation bar (the navy strip), mirroring FA's app chrome.
// The company button on the right opens an account dropdown (Company Details,
// Change Password, Logout).
import { ref } from 'vue'
import { useRouter, useRoute, RouterLink } from 'vue-router'
import Menu from 'primevue/menu'
import type { MenuItem } from 'primevue/menuitem'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()

// An item is active when the current route sits under its `to` path. Placeholder
// items (no `to`) are never active.
function isActive(item: NavItem): boolean {
  return item.to ? route.path.startsWith(item.to) : false
}

// Top nav items mirroring FreeAgent's chrome. An item with `to` renders as a real
// router link; the rest are placeholders (clickable-looking but inert) until those
// sections exist. "Expenses" is the active section and links to the expense list.
interface NavItem {
  label: string
  caret: boolean
  to?: string
}
const navItems: NavItem[] = [
  { label: 'Overview', caret: false },
  { label: 'Contacts', caret: false, to: '/contacts' },
  { label: 'Projects', caret: false, to: '/projects' },
  { label: 'Bills', caret: false },
  { label: 'Expenses', caret: false, to: '/expenses' },
  { label: 'Banking', caret: true },
  { label: 'Taxes', caret: true },
  { label: 'Accounting', caret: true },
]

// Account / organisation dropdown. ref holds the PrimeVue popup Menu instance.
const accountMenu = ref()
const accountItems = ref<MenuItem[]>([
  { label: 'Company Details', icon: 'pi pi-building', command: () => router.push('/company-details') },
  { label: 'My Details', icon: 'pi pi-user', command: () => router.push('/my-details') },
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
        <li v-for="item in navItems" :key="item.label" class="flex items-stretch">
          <!-- Items with a `to` render as a real <RouterLink>; the rest are inert. -->
          <component
            :is="item.to ? RouterLink : 'span'"
            :to="item.to"
            class="flex cursor-pointer items-center gap-1 whitespace-nowrap px-3 text-sm font-medium hover:bg-fa-nav-active"
            :class="{ 'bg-fa-nav-active': isActive(item) }"
          >
            {{ item.label }}
            <i v-if="item.caret" class="pi pi-angle-down text-[11px] opacity-[0.85]" />
          </component>
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
