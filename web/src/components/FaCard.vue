<script setup lang="ts">
// Reusable "card" panel matching FreeAgent's sections: a white box with a
// light-grey header strip (bold title + optional right-aligned note) and a body.
//
//   <FaCard title="Expense details" note="Required fields *">…</FaCard>
//
// The header is omitted entirely if no title and no #header slot are provided.
defineProps<{
  title?: string
  note?: string
}>()
</script>

<template>
  <section class="card">
    <header v-if="title || $slots.header" class="card__header">
      <h2 v-if="title" class="card__title">{{ title }}</h2>
      <slot name="header" />
      <span v-if="note" class="card__note">{{ note }}</span>
    </header>
    <div class="card__body">
      <slot />
    </div>
  </section>
</template>

<style scoped>
.card {
  background: #fff;
  border: 1px solid var(--fa-border);
  border-radius: 5px;
  margin-bottom: 20px;
  overflow: hidden;
}
.card__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 20px;
  background: var(--fa-card-header);
  border-bottom: 1px solid var(--fa-border);
}
.card__title {
  margin: 0;
  font-size: 15px;
  font-weight: 700;
  color: var(--fa-text);
}
.card__note {
  font-size: 13px;
  color: var(--fa-muted);
}
.card__body {
  padding: 22px 20px;
}
</style>
