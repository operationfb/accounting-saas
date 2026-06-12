<script setup lang="ts">
// Small coloured pill for an expense status. Variant classes map to the
// backend's status values (DRAFT / SUBMITTED / APPROVED / REJECTED / PAID).
// These are tag-specific one-off colours, so they stay as arbitrary values
// here rather than in the shared @theme tokens.
import { computed } from 'vue'

const props = defineProps<{
  status: string
}>()

const variants: Record<string, string> = {
  DRAFT: 'bg-[#eef1f4] text-[#5b6772] border-[#dde2e8]',
  SUBMITTED: 'bg-[#e8f1fb] text-[#1f6fd0] border-[#cfe2f7]',
  APPROVED: 'bg-[#eaf7e6] text-[#3f8038] border-[#cfe9c7]',
  REJECTED: 'bg-[#fdecec] text-[#c0392b] border-[#f6d3d0]',
  PAID: 'bg-[#e6f5f3] text-[#167d6e] border-[#c8e9e4]',
}

const variantClass = computed(() => variants[props.status] ?? variants.DRAFT)
</script>

<template>
  <span
    class="inline-block rounded-full border px-2.5 py-0.5 text-xs font-semibold tracking-wide"
    :class="variantClass"
    >{{ status }}</span
  >
</template>
