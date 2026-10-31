<template>
  <span class="prize-icon" :class="`prize-icon--${kind}`" :style="color ? { '--prize-color': color } : undefined" aria-hidden="true">
    <Icon :name="kind === 'balance' ? 'creditCard' : kind === 'subscription' ? 'sparkles' : 'gift'" class="h-6 w-6" />
    <span class="prize-icon-dot"></span>
  </span>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
defineProps<{ kind: 'balance' | 'subscription' | 'text'; color?: string }>()
</script>

<style scoped>
.prize-icon {
  --prize-color: #0f766e;
  position: relative;
  display: inline-flex;
  flex-shrink: 0;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  border-radius: 8px;
  color: var(--prize-color);
  background: color-mix(in srgb, var(--prize-color) 10%, white);
  border: 1px solid color-mix(in srgb, var(--prize-color) 20%, transparent);
}
.prize-icon--subscription { --prize-color: #2563eb; }
.prize-icon--text { --prize-color: #be185d; }
.prize-icon-dot {
  position: absolute;
  right: 5px;
  bottom: 5px;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--prize-color);
}
.dark .prize-icon {
  color: color-mix(in srgb, var(--prize-color) 40%, white);
  background: color-mix(in srgb, var(--prize-color) 18%, #171717);
}
</style>
