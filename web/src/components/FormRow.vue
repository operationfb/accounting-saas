<script setup lang="ts">
// A single form row: right-aligned label + control(s) on the right.
// Replaces the old repeated `.frow` CSS so the entry form stays Tailwind-first
// without long class strings on every row. The default slot holds the
// control(s) — and any help text, which stacks below them.
defineProps<{
  label?: string
  labelFor?: string
  required?: boolean
}>()
</script>

<template>
  <div class="grid grid-cols-1 gap-1.5 py-2 sm:grid-cols-[190px_minmax(0,1fr)] sm:items-center sm:gap-4">
    <label :for="labelFor" class="text-sm text-fa-text sm:text-right">
      <template v-if="label"
        >{{ label }}<span v-if="required" class="ml-0.5 text-fa-required">*</span></template
      >
    </label>
    <div class="flex min-w-0 flex-col gap-1.5">
      <slot />
    </div>
  </div>
</template>
