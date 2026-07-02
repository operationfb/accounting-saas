import { ref, onScopeDispose } from 'vue'

// useSpeechRecognition — a thin wrapper around the browser's Web Speech API for
// DICTATION (speech → text). It is feature-detected: `isSupported` is false in
// browsers without it (e.g. Firefox), so the caller can hide the mic button.
//
// The Web Speech typings are inconsistent across TS/DOM lib versions (and the
// webkit-prefixed constructor is usually missing), so we describe just the shape
// we use locally and reach the constructor through a narrow cast — no global
// type augmentation, no clashes with lib.dom.

interface SRAlternative {
  readonly transcript: string
}
interface SRResult {
  readonly isFinal: boolean
  readonly length: number
  readonly [index: number]: SRAlternative
}
interface SRResultList {
  readonly length: number
  readonly [index: number]: SRResult
}
interface SRResultEvent {
  readonly resultIndex: number
  readonly results: SRResultList
}
interface SRErrorEvent {
  readonly error: string
}
interface SpeechRecognitionLike {
  lang: string
  continuous: boolean
  interimResults: boolean
  maxAlternatives: number
  start(): void
  stop(): void
  abort(): void
  onresult: ((event: SRResultEvent) => void) | null
  onerror: ((event: SRErrorEvent) => void) | null
  onend: (() => void) | null
}
type SpeechRecognitionCtor = new () => SpeechRecognitionLike

function getRecognitionCtor(): SpeechRecognitionCtor | null {
  if (typeof window === 'undefined') return null
  const w = window as unknown as {
    SpeechRecognition?: SpeechRecognitionCtor
    webkitSpeechRecognition?: SpeechRecognitionCtor
  }
  return w.SpeechRecognition ?? w.webkitSpeechRecognition ?? null
}

export interface UseSpeechRecognitionOptions {
  lang?: string
  // Called as speech is transcribed. `text` is the FULL transcript for the
  // current listening session (finalised phrases + the live interim tail);
  // `isFinal` is true once nothing is still pending.
  onResult: (text: string, isFinal: boolean) => void
  onError?: (error: string) => void
}

export function useSpeechRecognition(options: UseSpeechRecognitionOptions) {
  const Ctor = getRecognitionCtor()
  const isSupported = Ctor !== null
  const isListening = ref(false)
  let recognition: SpeechRecognitionLike | null = null

  function ensure(): SpeechRecognitionLike | null {
    if (!Ctor) return null
    if (recognition) return recognition
    const r = new Ctor()
    r.lang = options.lang ?? 'en-GB'
    r.continuous = true // keep listening until the user stops
    r.interimResults = true
    r.maxAlternatives = 1

    r.onresult = (event) => {
      // In continuous mode `results` accumulates every phrase, so rebuild the
      // full transcript each time rather than tracking deltas.
      let final = ''
      let interim = ''
      for (let i = 0; i < event.results.length; i++) {
        const res = event.results[i]
        const txt = res[0]?.transcript ?? ''
        if (res.isFinal) final += txt
        else interim += txt
      }
      options.onResult((final + interim).trim(), interim === '')
    }
    r.onerror = (event) => {
      isListening.value = false
      options.onError?.(event.error)
    }
    r.onend = () => {
      isListening.value = false
    }

    recognition = r
    return r
  }

  function start() {
    const r = ensure()
    if (!r || isListening.value) return
    try {
      r.start()
      isListening.value = true
    } catch {
      // start() throws if it's already running — safe to ignore.
    }
  }

  function stop() {
    if (recognition && isListening.value) recognition.stop()
    isListening.value = false
  }

  function toggle() {
    if (isListening.value) stop()
    else start()
  }

  // Tear down when the owning component unmounts so the mic is released.
  onScopeDispose(() => {
    if (!recognition) return
    recognition.onresult = null
    recognition.onerror = null
    recognition.onend = null
    try {
      recognition.abort()
    } catch {
      /* ignore */
    }
  })

  return { isSupported, isListening, start, stop, toggle }
}
