<script setup lang="ts">
// The Tala prompt box: a textarea with a dictation MIC inside it (bottom-left)
// and the SEND arrow inside it (bottom-right). Shared by both the empty-state
// (big) and active-conversation (compact) composers, so the voice wiring lives
// in one place. v-model carries the text; @send fires on the arrow or Enter.
import { useSpeechRecognition } from '@/composables/useSpeechRecognition'

const props = withDefaults(
  defineProps<{
    modelValue: string
    placeholder?: string
    rows?: number
    canSend?: boolean
  }>(),
  { placeholder: 'Ask Tala…', rows: 2, canSend: false },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'send'): void
}>()

// Dictation. `base` holds whatever was already in the box when the mic started,
// so speech is appended rather than replacing typed text.
let base = ''
const {
  isSupported: micSupported,
  isListening: micListening,
  toggle: recognitionToggle,
} = useSpeechRecognition({
  onResult: (text) => emit('update:modelValue', base ? `${base} ${text}` : text),
})

function toggleMic() {
  if (!micListening.value) base = props.modelValue.trim()
  recognitionToggle()
}

function onInput(e: Event) {
  emit('update:modelValue', (e.target as HTMLTextAreaElement).value)
}

function onKeydown(e: KeyboardEvent) {
  // Enter sends; Shift+Enter inserts a newline.
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    if (props.canSend) emit('send')
  }
}
</script>

<template>
  <div class="relative">
    <textarea
      :value="modelValue"
      :rows="rows"
      :placeholder="placeholder"
      class="w-full resize-none rounded-2xl border border-gray-300 bg-white py-3 pr-14 text-base shadow-sm focus:border-[#2d6a4f] focus:outline-none focus:ring-1 focus:ring-[#2d6a4f]"
      :class="micSupported ? 'pl-14' : 'pl-5'"
      @input="onInput"
      @keydown="onKeydown"
    ></textarea>

    <!-- Dictation mic, bottom-left. Hidden where the browser has no speech API. -->
    <button
      v-if="micSupported"
      type="button"
      :aria-label="micListening ? 'Stop dictation' : 'Dictate'"
      :title="micListening ? 'Stop dictation' : 'Dictate'"
      class="absolute bottom-3 left-3 flex h-9 w-9 items-center justify-center rounded-full transition"
      :class="
        micListening
          ? 'animate-pulse bg-red-500 text-white'
          : 'bg-gray-100 text-gray-600 hover:bg-gray-200'
      "
      @click="toggleMic"
    >
      <svg viewBox="0 0 24 24" class="h-5 w-5" fill="none" stroke="currentColor" stroke-width="2">
        <path
          d="M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3z"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
        <path d="M5 11a7 7 0 0 0 14 0M12 18v3" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    </button>

    <!-- Send arrow, bottom-right. -->
    <button
      type="button"
      aria-label="Send"
      class="absolute bottom-3 right-3 flex h-9 w-9 items-center justify-center rounded-full bg-[#2d6a4f] text-white transition hover:bg-[#245A42] disabled:cursor-not-allowed disabled:opacity-40"
      :disabled="!canSend"
      @click="emit('send')"
    >
      <svg viewBox="0 0 24 24" class="h-5 w-5" fill="none" stroke="currentColor" stroke-width="2.5">
        <path d="M12 19V5M5 12l7-7 7 7" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    </button>
  </div>
</template>
