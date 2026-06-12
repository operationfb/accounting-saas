<script setup lang="ts">
// Reusable "card" panel matching FA's sections: a white box with a light-grey
// header strip (bold title + optional right-aligned note) and a body.
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
  <section class="mb-5 overflow-hidden rounded-[5px] border border-fa-border bg-white">
    <header
      v-if="title || $slots.header"
      class="flex items-center justify-between gap-3 border-b border-fa-border bg-fa-card-header px-5 py-3"
    >
      <h2 v-if="title" class="text-[15px] font-bold text-fa-text">{{ title }}</h2>
      <slot name="header" />
      <span v-if="note" class="text-[13px] text-fa-muted">{{ note }}</span>
    </header>
    <div class="px-5 py-[22px]">
      <slot />
    </div>
  </section>
</template>
