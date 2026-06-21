<script lang="ts" setup>
import { computed } from 'vue'
import { renderMarkdown } from '../lib/markdown'

// Render del Markdown de la IA. El HTML ya viene sanitizado por renderMarkdown
// (DOMPurify), asi que es seguro inyectarlo con v-html.
const props = defineProps<{ text: string }>()
const html = computed(() => renderMarkdown(props.text))
</script>

<template>
  <div class="md" v-html="html"></div>
</template>
