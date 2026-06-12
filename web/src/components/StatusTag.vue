<script setup lang="ts">
// Small coloured pill for an expense status. Colours map to the backend's
// status values (DRAFT / SUBMITTED / APPROVED / REJECTED / PAID).
defineProps<{
  status: string
}>()
</script>

<template>
  <span class="tag" :data-status="status">{{ status }}</span>
</template>

<style scoped>
.tag {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.3px;
  border: 1px solid transparent;
}
.tag[data-status='DRAFT'] {
  background: #eef1f4;
  color: #5b6772;
  border-color: #dde2e8;
}
.tag[data-status='SUBMITTED'] {
  background: #e8f1fb;
  color: #1f6fd0;
  border-color: #cfe2f7;
}
.tag[data-status='APPROVED'] {
  background: #eaf7e6;
  color: #3f8038;
  border-color: #cfe9c7;
}
.tag[data-status='REJECTED'] {
  background: #fdecec;
  color: #c0392b;
  border-color: #f6d3d0;
}
.tag[data-status='PAID'] {
  background: #e6f5f3;
  color: #167d6e;
  border-color: #c8e9e4;
}
</style>
