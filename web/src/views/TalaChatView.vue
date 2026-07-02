<script setup lang="ts">
// Tala — the AI accountant assistant. A simple prompt screen: a transcript of
// prompt/reply bubbles, example-prompt chips on the empty state, a "thinking"
// indicator, a prompt box, and confirmation cards for any guarded-write proposals.
//
// The conversation is stateless on the server, so we keep the running transcript
// here and send it (as {role, content}) with every turn.
import { ref, nextTick, computed } from 'vue'
import { sendTalaChat } from '@/services/tala.service'
import type { TalaChatMessage, TalaProposedAction } from '@/types/tala'
import type { ApiError } from '@/lib/api'
import AppLayout from '@/layouts/AppLayout.vue'
import TalaProposalCard from '@/components/TalaProposalCard.vue'

interface Turn {
  role: 'user' | 'assistant'
  content: string
  proposals?: TalaProposedAction[]
  toolCalls?: string[]
}

const transcript = ref<Turn[]>([])
const input = ref('')
const loading = ref(false)
const error = ref('')
const scroller = ref<HTMLElement | null>(null)

const examples = [
  'What do I owe in VAT?',
  'Show my overdue invoices',
  "Summarise this month's expenses",
  "What's my cash position?",
]

const canSend = computed(() => input.value.trim().length > 0 && !loading.value)

async function scrollToBottom() {
  await nextTick()
  scroller.value?.scrollTo({ top: scroller.value.scrollHeight, behavior: 'smooth' })
}

async function send() {
  const text = input.value.trim()
  if (!text || loading.value) return
  input.value = ''
  error.value = ''
  transcript.value.push({ role: 'user', content: text })
  void scrollToBottom()

  loading.value = true
  try {
    // The API takes the full history; strip our display-only fields.
    const history: TalaChatMessage[] = transcript.value.map((t) => ({ role: t.role, content: t.content }))
    const res = await sendTalaChat(history)
    transcript.value.push({
      role: 'assistant',
      content: res.reply || '(no answer)',
      proposals: res.proposed_actions?.length ? res.proposed_actions : undefined,
      toolCalls: res.tool_calls?.length ? res.tool_calls : undefined,
    })
  } catch (e) {
    error.value = (e as ApiError)?.message ?? 'Tala is unavailable right now.'
  } finally {
    loading.value = false
    void scrollToBottom()
  }
}

function useExample(text: string) {
  input.value = text
  void send()
}

function onKeydown(e: KeyboardEvent) {
  // Enter sends; Shift+Enter inserts a newline.
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    void send()
  }
}

function dismissProposal(turn: Turn, index: number) {
  turn.proposals?.splice(index, 1)
}
</script>

<template>
  <AppLayout>
    <div class="mx-auto flex h-[calc(100vh-13rem)] min-h-[28rem] max-w-3xl flex-col">
    <header class="mb-3">
      <h1 class="text-xl font-semibold text-gray-800">Tala</h1>
      <p class="text-sm text-gray-500">Your AI accountant assistant</p>
    </header>

    <div
      ref="scroller"
      class="flex-1 space-y-4 overflow-y-auto rounded-lg border border-gray-200 bg-gray-50 p-4"
    >
      <!-- empty state -->
      <div v-if="transcript.length === 0" class="mx-auto max-w-md pt-8 text-center">
        <div
          class="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-full bg-[#2d6a4f] text-lg font-bold text-white"
        >
          T
        </div>
        <h2 class="text-lg font-semibold text-gray-800">Hi, I'm Tala 👋</h2>
        <p class="mt-1 text-sm text-gray-500">
          Ask about your expenses, invoices, VAT or cash — or ask me to help you get something done.
        </p>
        <div class="mt-4 flex flex-wrap justify-center gap-2">
          <button
            v-for="ex in examples"
            :key="ex"
            class="rounded-full border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-700 hover:border-[#2d6a4f] hover:text-[#2d6a4f]"
            @click="useExample(ex)"
          >
            {{ ex }}
          </button>
        </div>
      </div>

      <!-- transcript -->
      <div
        v-for="(turn, i) in transcript"
        :key="i"
        class="flex flex-col"
        :class="turn.role === 'user' ? 'items-end' : 'items-start'"
      >
        <div
          class="max-w-[85%] whitespace-pre-wrap rounded-2xl px-4 py-2 text-sm"
          :class="
            turn.role === 'user'
              ? 'bg-[#2d6a4f] text-white'
              : 'bg-white text-gray-800 shadow-sm ring-1 ring-gray-200'
          "
        >
          {{ turn.content }}
        </div>
        <p v-if="turn.toolCalls" class="mt-1 px-1 text-xs text-gray-400">
          Checked: {{ turn.toolCalls.join(', ') }}
        </p>
        <div v-if="turn.proposals" class="w-full max-w-[85%]">
          <TalaProposalCard
            v-for="(p, j) in turn.proposals"
            :key="j"
            :proposal="p"
            @dismissed="dismissProposal(turn, j)"
          />
        </div>
      </div>

      <!-- thinking indicator -->
      <div v-if="loading" class="flex items-start">
        <div
          class="rounded-2xl bg-white px-4 py-3 text-sm text-gray-400 shadow-sm ring-1 ring-gray-200"
        >
          Tala is thinking…
        </div>
      </div>
    </div>

    <p v-if="error" class="mt-2 text-sm text-red-600">{{ error }}</p>

    <form class="mt-3 flex items-end gap-2" @submit.prevent="send">
      <textarea
        v-model="input"
        rows="1"
        placeholder="Ask Tala anything about your books…"
        class="max-h-40 min-h-[44px] flex-1 resize-none rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-[#2d6a4f] focus:outline-none focus:ring-1 focus:ring-[#2d6a4f]"
        @keydown="onKeydown"
      ></textarea>
      <button
        type="submit"
        class="h-[44px] rounded-lg bg-[#2d6a4f] px-4 text-sm font-medium text-white hover:bg-[#245A42] disabled:opacity-50"
        :disabled="!canSend"
      >
        Send
      </button>
    </form>
    </div>
  </AppLayout>
</template>
