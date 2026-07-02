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
    <!-- The root width sizes the ACTIVE conversation (transcript + composer, both
         full-width here). The empty state stays narrow via its own inner max-w-2xl. -->
    <div class="mx-auto flex h-[calc(100vh-12rem)] min-h-[30rem] max-w-5xl flex-col">
      <!-- ================================================================ -->
      <!-- EMPTY STATE — Claude-style first screen: a centred greeting, a    -->
      <!-- big prompt box with the send arrow INSIDE it, and the suggestion  -->
      <!-- chips underneath.                                                 -->
      <!-- ================================================================ -->
      <div
        v-if="transcript.length === 0"
        class="flex flex-1 flex-col items-center justify-center px-2"
      >
        <div class="mb-6 flex flex-col items-center text-center">
          <div
            class="mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-[#2d6a4f] text-2xl font-bold text-white"
          >
            T
          </div>
          <h1 class="text-2xl font-semibold text-gray-800">Hi, I'm Tala 👋</h1>
          <p class="mt-1 text-gray-500">
            Your AI accountant — ask about your expenses, invoices, VAT or cash.
          </p>
        </div>

        <div class="w-full max-w-2xl">
          <!-- big prompt box; the send button sits inside, bottom-right -->
          <div class="relative">
            <textarea
              v-model="input"
              rows="3"
              placeholder="Ask Tala anything about your books…"
              class="w-full resize-none rounded-2xl border border-gray-300 bg-white px-5 py-4 pr-14 text-base shadow-sm focus:border-[#2d6a4f] focus:outline-none focus:ring-1 focus:ring-[#2d6a4f]"
              @keydown="onKeydown"
            ></textarea>
            <button
              type="button"
              aria-label="Send"
              class="absolute bottom-3 right-3 flex h-9 w-9 items-center justify-center rounded-full bg-[#2d6a4f] text-white transition hover:bg-[#245A42] disabled:cursor-not-allowed disabled:opacity-40"
              :disabled="!canSend"
              @click="send"
            >
              <svg
                viewBox="0 0 24 24"
                class="h-5 w-5"
                fill="none"
                stroke="currentColor"
                stroke-width="2.5"
              >
                <path d="M12 19V5M5 12l7-7 7 7" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
            </button>
          </div>

          <!-- suggestion chips, under the prompt box -->
          <div class="mt-3 flex flex-wrap justify-center gap-2">
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
      </div>

      <!-- ================================================================ -->
      <!-- ACTIVE CONVERSATION — the transcript, then a bottom composer that -->
      <!-- reuses the same arrow-in-box prompt style.                        -->
      <!-- ================================================================ -->
      <template v-else>
        <div
          ref="scroller"
          class="flex-1 space-y-4 overflow-y-auto rounded-lg border border-gray-200 bg-gray-50 p-4"
        >
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

        <div class="relative mt-3">
          <textarea
            v-model="input"
            rows="2"
            placeholder="Reply to Tala…"
            class="w-full resize-none rounded-2xl border border-gray-300 bg-white px-5 py-3 pr-14 text-base shadow-sm focus:border-[#2d6a4f] focus:outline-none focus:ring-1 focus:ring-[#2d6a4f]"
            @keydown="onKeydown"
          ></textarea>
          <button
            type="button"
            aria-label="Send"
            class="absolute bottom-3 right-3 flex h-9 w-9 items-center justify-center rounded-full bg-[#2d6a4f] text-white transition hover:bg-[#245A42] disabled:cursor-not-allowed disabled:opacity-40"
            :disabled="!canSend"
            @click="send"
          >
            <svg
              viewBox="0 0 24 24"
              class="h-5 w-5"
              fill="none"
              stroke="currentColor"
              stroke-width="2.5"
            >
              <path d="M12 19V5M5 12l7-7 7 7" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </button>
        </div>
      </template>
    </div>
  </AppLayout>
</template>
