<script lang="ts" setup>
import { nextTick, onMounted, reactive, ref } from 'vue'
import { learning } from '../../../wailsjs/go/models'

const props = defineProps<{
  runs: learning.Run[]
  lessons: learning.Lesson[]
  pending: Set<string>
}>()
const emit = defineEmits<{
  close: []
  approve: [id: string, candidate: learning.Candidate]
  reject: [id: string]
  cancel: [id: string]
  retry: [id: string]
  toggleLesson: [id: string, enabled: boolean]
  deleteLesson: [id: string]
}>()
const closeButton = ref<HTMLButtonElement | null>(null)
const dialog = ref<HTMLElement | null>(null)
const drafts = reactive<Record<string, learning.Candidate>>({})

function draft(run: learning.Run): learning.Candidate {
  if (!drafts[run.id] && run.candidate)
    drafts[run.id] = new learning.Candidate({
      ...run.candidate,
      evidence: run.candidate.evidence,
    })
  return drafts[run.id]
}
function busy(prefix: string, id: string) {
  return props.pending.has(`${prefix}:${id}`)
}
function timestamp(value: unknown) {
  return value ? new Date(String(value)).toLocaleString() : '—'
}
function duration(run: learning.Run) {
  if (!run.startedAt || !run.finishedAt) return '—'
  return `${Math.max(0, new Date(String(run.finishedAt)).getTime() - new Date(String(run.startedAt)).getTime())} ms`
}
function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') emit('close')
  if (event.key === 'Tab' && dialog.value) {
    const controls = Array.from(
      dialog.value.querySelectorAll<HTMLElement>(
        'button:not([disabled]), textarea:not([disabled]), [tabindex="0"]',
      ),
    )
    if (!controls.length) return
    const first = controls[0]
    const last = controls[controls.length - 1]
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }
}
onMounted(() => nextTick(() => closeButton.value?.focus()))
</script>

<template>
  <section
    ref="dialog"
    role="dialog"
    aria-modal="true"
    aria-labelledby="learning-audit-title"
    class="absolute inset-0 z-30 overflow-y-auto bg-white p-6"
    @keydown="onKeydown"
  >
    <header class="mx-auto flex max-w-3xl items-center justify-between">
      <h2 id="learning-audit-title" class="text-xl font-semibold">
        Learning audit
      </h2>
      <button
        ref="closeButton"
        type="button"
        aria-label="Close learning audit"
        @click="$emit('close')"
      >
        Close
      </button>
    </header>
    <div class="mx-auto mt-6 max-w-3xl" aria-live="polite">
      <h3 class="font-semibold">Approved lessons</h3>
      <p v-if="lessons.length === 0" class="text-sm text-zinc-500">
        No approved lessons.
      </p>
      <article
        v-for="lesson in lessons"
        :key="lesson.id"
        class="mt-3 rounded-xl border border-zinc-200 p-4"
      >
        <strong>{{ lesson.candidate.statement }}</strong>
        <p class="text-sm">Scope: {{ lesson.candidate.scope }}</p>
        <p class="mt-1 text-xs text-zinc-500">
          Run {{ lesson.runID }} · created {{ timestamp(lesson.createdAt) }}
        </p>
        <div class="mt-3 flex gap-2">
          <button
            type="button"
            :disabled="busy('lesson', lesson.id) || lesson.deleted"
            :aria-label="`${lesson.enabled ? 'Disable' : 'Enable'} lesson ${lesson.candidate.statement}`"
            @click="$emit('toggleLesson', lesson.id, !lesson.enabled)"
          >
            {{ lesson.enabled ? 'Disable' : 'Enable' }}
          </button>
          <button
            type="button"
            :disabled="busy('delete', lesson.id) || lesson.deleted"
            :aria-label="`Delete lesson ${lesson.candidate.statement}`"
            @click="$emit('deleteLesson', lesson.id)"
          >
            Delete
          </button>
        </div>
      </article>

      <h3 class="mt-8 font-semibold">Runs</h3>
      <p v-if="runs.length === 0" class="text-sm text-zinc-500">
        No learning runs yet.
      </p>
      <article
        v-for="run in runs"
        :key="run.id"
        class="mt-3 rounded-xl border border-zinc-200 p-4"
        tabindex="0"
      >
        <div class="flex justify-between gap-4">
          <strong>{{ run.status }}</strong
          ><span class="text-sm text-zinc-500"
            >{{ run.providerID }} / {{ run.model }}</span
          >
        </div>
        <p class="mt-2 text-sm">
          Session {{ run.sessionID }} through sequence {{ run.cutSeq }} ·
          {{
            run.input.truncated
              ? 'evidence truncated'
              : 'complete bounded evidence'
          }}
        </p>
        <details class="mt-3">
          <summary :aria-label="`Show captured evidence for run ${run.id}`">
            Captured evidence ({{ run.input.messages.length }})
          </summary>
          <ol class="mt-2 grid gap-2">
            <li
              v-for="message in run.input.messages"
              :key="`${message.seq}:${message.role}`"
              class="rounded bg-zinc-50 p-2"
            >
              <strong class="text-xs"
                >Sequence {{ message.seq }} · {{ message.role }}</strong
              >
              <pre class="mt-1 whitespace-pre-wrap text-sm">{{
                message.text
              }}</pre>
            </li>
          </ol>
        </details>
        <p class="mt-1 text-xs text-zinc-500">
          Queued {{ timestamp(run.createdAt) }} · started
          {{ timestamp(run.startedAt) }} · finished
          {{ timestamp(run.finishedAt) }} · duration {{ duration(run) }}
        </p>
        <template v-if="run.candidate">
          <label class="mt-3 block text-sm"
            >Statement<textarea
              v-model="draft(run).statement"
              :aria-label="`Edit statement for run ${run.id}`"
              class="block w-full rounded border p-2"
            />
          </label>
          <label class="mt-2 block text-sm"
            >Scope<textarea
              v-model="draft(run).scope"
              :aria-label="`Edit scope for run ${run.id}`"
              class="block w-full rounded border p-2"
            />
          </label>
          <label class="mt-2 block text-sm"
            >Do not apply<textarea
              v-model="draft(run).exceptions"
              :aria-label="`Edit exceptions for run ${run.id}`"
              class="block w-full rounded border p-2"
            />
          </label>
          <ul class="mt-2 list-disc pl-5 text-sm">
            <li v-for="evidence in run.candidate.evidence" :key="evidence.seq">
              Sequence {{ evidence.seq }}: {{ evidence.summary }}
            </li>
          </ul>
          <div v-if="run.status === 'ready'" class="mt-3 flex gap-2">
            <button
              type="button"
              :disabled="busy('approve', run.id)"
              :aria-label="`Add candidate from run ${run.id}`"
              @click="$emit('approve', run.id, run.candidate)"
            >
              Add
            </button>
            <button
              type="button"
              :disabled="busy('approve', run.id)"
              :aria-label="`Edit and add candidate from run ${run.id}`"
              @click="$emit('approve', run.id, draft(run))"
            >
              Edit &amp; Add
            </button>
            <button
              type="button"
              :disabled="busy('reject', run.id)"
              :aria-label="`Reject candidate from run ${run.id}`"
              @click="$emit('reject', run.id)"
            >
              Reject
            </button>
          </div>
        </template>
        <p v-if="run.noCandidateReason" class="mt-2">
          {{ run.noCandidateReason }}
        </p>
        <p v-if="run.error" role="alert" class="mt-2 text-red-700">
          {{ run.error }}
        </p>
        <p class="mt-3 text-xs text-zinc-500">
          Cost: {{ run.usage?.inputTokens ?? 0 }} input,
          {{ run.usage?.outputTokens ?? 0 }} output,
          {{ run.usage?.reasoningTokens ?? 0 }} reasoning tokens · decision
          {{ timestamp(run.decidedAt) }}
        </p>
        <button
          v-if="
            run.status === 'queued' ||
            run.status === 'running' ||
            run.status === 'cancelling'
          "
          type="button"
          :disabled="busy('cancel', run.id) || run.status === 'cancelling'"
          :aria-label="`Cancel learning run ${run.id}`"
          @click="$emit('cancel', run.id)"
        >
          Cancel
        </button>
        <button
          v-if="
            run.status === 'failed' ||
            run.status === 'cancelled' ||
            run.status === 'interrupted'
          "
          type="button"
          :disabled="busy('retry', run.id)"
          :aria-label="`Retry learning run ${run.id}`"
          @click="$emit('retry', run.id)"
        >
          Retry
        </button>
      </article>
    </div>
  </section>
</template>
