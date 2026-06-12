<script setup lang="ts">
// Top navigation bar (the navy strip), mirroring FreeAgent's app chrome.
// Static for now — the items are not wired to routes. "My Money" is shown as
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
</script>

<template>
  <header class="topbar">
    <nav class="topbar__inner">
      <ul class="topbar__nav">
        <li
          v-for="item in navItems"
          :key="item.label"
          class="topbar__item"
          :class="{ 'topbar__item--active': item.active }"
        >
          {{ item.label }}
          <i v-if="item.caret" class="pi pi-angle-down topbar__caret" />
        </li>
      </ul>

      <div class="topbar__right">
        <button type="button" class="topbar__icon" aria-label="Search">
          <i class="pi pi-search" />
        </button>
        <button type="button" class="topbar__icon topbar__icon--badge" aria-label="Notifications">
          <i class="pi pi-bell" />
          <span class="topbar__badge" />
        </button>
        <button type="button" class="topbar__company">
          AXION LONDON LIMITED
          <i class="pi pi-angle-down topbar__caret" />
        </button>
      </div>
    </nav>
  </header>
</template>

<style scoped>
.topbar {
  background: var(--fa-nav);
  color: #fff;
}
.topbar__inner {
  max-width: 1200px;
  margin: 0 auto;
  padding: 0 16px;
  height: 46px;
  display: flex;
  align-items: stretch;
  justify-content: space-between;
}
.topbar__nav {
  display: flex;
  align-items: stretch;
  gap: 2px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.topbar__item {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 0 12px;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  white-space: nowrap;
}
.topbar__item:hover {
  background: var(--fa-nav-active);
}
.topbar__item--active {
  background: var(--fa-nav-active);
}
.topbar__caret {
  font-size: 11px;
  opacity: 0.85;
}
.topbar__right {
  display: flex;
  align-items: center;
  gap: 6px;
}
.topbar__icon {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  border: 0;
  background: transparent;
  color: #fff;
  border-radius: 4px;
  cursor: pointer;
}
.topbar__icon:hover {
  background: var(--fa-nav-active);
}
.topbar__badge {
  position: absolute;
  top: 7px;
  right: 7px;
  width: 8px;
  height: 8px;
  background: var(--fa-green);
  border-radius: 50%;
}
.topbar__company {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 0 8px;
  height: 34px;
  border: 0;
  background: transparent;
  color: #fff;
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 0.2px;
  cursor: pointer;
  border-radius: 4px;
}
.topbar__company:hover {
  background: var(--fa-nav-active);
}
</style>
