<script setup lang="ts">
// Top navigation bar (the navy strip), mirroring FA's app chrome.
// Static for now — items are not wired to routes. "My Money" is the active
// section because expenses live under it.
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
        <button
          type="button"
          class="inline-flex h-[34px] items-center gap-1.5 rounded px-2 text-[13px] font-semibold text-white hover:bg-fa-nav-active"
        >
          AXION LONDON LIMITED
          <i class="pi pi-angle-down text-[11px] opacity-[0.85]" />
        </button>
      </div>
    </nav>
  </header>
</template>
