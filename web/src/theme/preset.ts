import { definePreset } from '@primevue/themes'
import Aura from '@primevue/themes/aura'

// FreeAgent-inspired PrimeVue theme.
//
// We start from PrimeVue's "Aura" preset and override only the PRIMARY colour
// ramp so buttons/inputs pick up FreeAgent's green (#79CC6E) instead of Aura's
// default blue. FreeAgent's green "Log in" button uses near-black label text,
// so we set the primary contrast colour to #181A1B rather than white.
//
// Everything else (spacing, radii, focus rings, component internals) is left to
// Aura — we only nudge the brand colour.
const FreeAgentPreset = definePreset(Aura, {
  semantic: {
    primary: {
      50: '#eef9eb',
      100: '#d6f0cf',
      200: '#bce6b1',
      300: '#a0db90',
      400: '#8ad27c',
      500: '#79cc6e', // FreeAgent green
      600: '#69bf5e',
      700: '#54a64b',
      800: '#3f8038',
      900: '#2e5e29',
      950: '#1b3a18',
    },
    colorScheme: {
      light: {
        primary: {
          color: '{primary.500}',
          contrastColor: '#181a1b', // dark label on the green button, like FreeAgent
          hoverColor: '{primary.600}',
          activeColor: '{primary.700}',
        },
      },
    },
  },
})

export default FreeAgentPreset
