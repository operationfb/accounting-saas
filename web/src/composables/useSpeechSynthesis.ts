import { ref, onScopeDispose } from 'vue'

// useSpeechSynthesis — a thin wrapper around the browser's SpeechSynthesis API
// for READ-ALOUD (text → speech). Widely supported (including Firefox), but still
// feature-detected so the caller can hide the speaker button where it's missing.
// `SpeechSynthesisUtterance` / `window.speechSynthesis` are in the standard DOM
// typings, so no ambient declarations are needed here.

export function useSpeechSynthesis(lang = 'en-GB') {
  const isSupported = typeof window !== 'undefined' && 'speechSynthesis' in window
  const isSpeaking = ref(false)

  function speak(text: string) {
    const trimmed = text.trim()
    if (!isSupported || !trimmed) return
    // Cancel anything already speaking so the buttons never overlap.
    window.speechSynthesis.cancel()

    const utterance = new SpeechSynthesisUtterance(trimmed)
    utterance.lang = lang
    utterance.onend = () => {
      isSpeaking.value = false
    }
    utterance.onerror = () => {
      isSpeaking.value = false
    }
    isSpeaking.value = true
    window.speechSynthesis.speak(utterance)
  }

  function stop() {
    if (!isSupported) return
    window.speechSynthesis.cancel()
    isSpeaking.value = false
  }

  // Stop any in-flight speech when the owning component unmounts.
  onScopeDispose(stop)

  return { isSupported, isSpeaking, speak, stop }
}
