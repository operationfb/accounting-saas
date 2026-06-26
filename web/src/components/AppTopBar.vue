<script setup lang="ts">
// Top navigation bar (the navy strip), mirroring FA's app chrome.
// The company button on the right opens an account dropdown (Company Details,
// Change Password, Logout).
import { computed, ref } from 'vue'
import { useRouter, useRoute, RouterLink } from 'vue-router'
import Menu from 'primevue/menu'
import type { MenuItem } from 'primevue/menuitem'
import { useAuthStore } from '@/stores/auth'

const isMobileMenuOpen = ref(false)
function toggleMobileMenu() {
  isMobileMenuOpen.value = !isMobileMenuOpen.value
}
function closeMobileMenu() {
  isMobileMenuOpen.value = false
}

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()

// A nav item is either a direct link (`to`) or a dropdown group (`children`).
// Mirroring FreeAgent: Invoices/Projects live under "Money In" and Expenses/Bills
// under "Money Out"; Contacts, Banking and VAT stay flat links.
interface NavChild {
  label: string
  to: string
}
interface NavItem {
  label: string
  to?: string // direct link (Contacts, Banking, VAT)
  children?: NavChild[] // dropdown group (Money In, Money Out)
}
const navItems: NavItem[] = [
  { label: 'Overview', to: '/overview' },
  { label: 'Contacts', to: '/contacts' },
  {
    label: 'Money In',
    children: [
      { label: 'Invoices', to: '/invoices' },
      { label: 'Projects', to: '/projects' },
    ],
  },
  {
    label: 'Money Out',
    children: [
      { label: 'Expenses', to: '/expenses' },
      { label: 'Bills', to: '/bills' },
    ],
  },
  { label: 'Banking', to: '/bank-accounts' },
  { label: 'VAT', to: '/vat-returns' },
]

// A direct item is active when the route sits under its `to`; a group is active
// when any of its children are (so "Money In" highlights while on /invoices).
function isChildActive(child: NavChild): boolean {
  return route.path.startsWith(child.to)
}
function isActive(item: NavItem): boolean {
  if (item.to) return route.path.startsWith(item.to)
  return (item.children ?? []).some(isChildActive)
}

// Account / organisation dropdown. ref holds the PrimeVue popup Menu instance.
const accountMenu = ref()
// Computed so the owner/admin-only Integrations item can be included by role.
const accountItems = computed<MenuItem[]>(() => [
  { label: 'Company Details', icon: 'pi pi-building', command: () => router.push('/company-details') },
  { label: 'VAT Registration', icon: 'pi pi-percentage', command: () => router.push('/vat-registration') },
  { label: 'My Details', icon: 'pi pi-user', command: () => router.push('/my-details') },
  // Managing integrations (FreeAgent, …) is owner/admin only — hidden otherwise.
  ...(auth.isOrgAdmin
    ? [
        {
          label: 'Integrations',
          icon: 'pi pi-link',
          command: () => router.push('/settings/integrations'),
        },
      ]
    : []),
  { label: 'Change Password', icon: 'pi pi-key', command: () => changePassword() },
  // Set the destructive sign-out apart from the navigation items with a divider.
  { separator: true },
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
  <header class="relative bg-fa-nav text-white">
    <nav class="mx-auto flex h-[46px] max-w-[1200px] items-stretch justify-between px-4">
      <!-- Mobile hamburger + desktop nav items, grouped flush-left -->
      <div class="flex items-stretch">
        <!-- Hamburger: phones only -->
        <button
          type="button"
          class="block sm:hidden inline-flex h-[46px] w-[34px] items-center justify-center text-white"
          aria-label="Toggle navigation menu"
          @click="toggleMobileMenu"
        >
          <i :class="isMobileMenuOpen ? 'pi pi-times' : 'pi pi-bars'" />
        </button>

        <!-- Desktop nav items: hidden on phones, visible on sm+ -->
        <ul class="hidden sm:flex items-stretch gap-0.5">
          <li
            v-for="item in navItems"
            :key="item.label"
            class="group relative flex items-stretch"
          >
            <!-- Direct link (Contacts, Banking, VAT). -->
            <RouterLink
              v-if="item.to"
              :to="item.to"
              class="flex cursor-pointer items-center gap-1 whitespace-nowrap px-3 text-sm font-medium hover:bg-fa-nav-active"
              :class="{ 'bg-fa-nav-active': isActive(item) }"
            >
              {{ item.label }}
            </RouterLink>

            <!-- Dropdown group (Money In, Money Out): hover reveals a FA-style
                 white panel of links. Pure CSS (group-hover) — opens on hover. -->
            <template v-else>
              <span
                class="flex cursor-pointer items-center gap-1 whitespace-nowrap px-3 text-sm font-medium group-hover:bg-fa-nav-active"
                :class="{ 'bg-fa-nav-active': isActive(item) }"
              >
                {{ item.label }}
                <i
                  class="pi pi-angle-down text-[11px] opacity-[0.85] transition-transform duration-150 group-hover:rotate-180"
                />
              </span>
              <!-- The panel sits flush under the bar (top-full, no gap) so the
                   hover isn't lost moving into it. Border + shadow as requested. -->
              <div
                class="invisible absolute left-0 top-full z-50 min-w-[190px] translate-y-1 pt-px opacity-0 transition duration-150 group-hover:visible group-hover:translate-y-0 group-hover:opacity-100"
              >
                <ul
                  class="overflow-hidden rounded-md border border-fa-border bg-white py-1 text-fa-text shadow-lg"
                >
                  <li v-for="child in item.children" :key="child.label">
                    <RouterLink
                      :to="child.to"
                      class="block whitespace-nowrap px-4 py-2 text-sm hover:bg-[#f3f6f9]"
                      :class="isChildActive(child) ? 'font-semibold text-fa-green' : 'text-fa-text'"
                    >
                      {{ child.label }}
                    </RouterLink>
                  </li>
                </ul>
              </div>
            </template>
          </li>
        </ul>
      </div>

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
          class="inline-flex h-[34px] items-center gap-1.5 rounded px-2 text-[13px] font-normal capitalize text-white hover:bg-fa-nav-active"
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

    <!-- Mobile nav dropdown: full-width panel below the bar, phones only -->
    <div
      v-if="isMobileMenuOpen"
      class="absolute left-0 right-0 top-[46px] z-50 bg-fa-nav sm:hidden"
    >
      <ul class="border-t border-white/10 py-1">
        <template v-for="item in navItems" :key="item.label">
          <!-- Direct link -->
          <li v-if="item.to">
            <RouterLink
              :to="item.to"
              class="flex cursor-pointer items-center gap-2 px-5 py-3 text-sm font-medium hover:bg-fa-nav-active"
              :class="{ 'bg-fa-nav-active': isActive(item) }"
              @click="closeMobileMenu"
            >
              {{ item.label }}
            </RouterLink>
          </li>
          <!-- Group: a muted header + its children indented beneath it. -->
          <li v-else>
            <div
              class="px-5 pb-1 pt-3 text-xs font-semibold uppercase tracking-wide text-white/50"
            >
              {{ item.label }}
            </div>
            <RouterLink
              v-for="child in item.children"
              :key="child.label"
              :to="child.to"
              class="flex cursor-pointer items-center gap-2 px-8 py-2.5 text-sm font-medium hover:bg-fa-nav-active"
              :class="{ 'bg-fa-nav-active': isChildActive(child) }"
              @click="closeMobileMenu"
            >
              {{ child.label }}
            </RouterLink>
          </li>
        </template>
      </ul>
    </div>
  </header>
</template>
