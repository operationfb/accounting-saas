import { definePreset } from '@primevue/themes'
import Aura from '@primevue/themes/aura'

// Kontala-branded PrimeVue theme.
//
// We start from PrimeVue's "Aura" preset and override the PRIMARY colour so all
// buttons (and other primary-tinted controls) use Kontala's brand green — the
// same values kontala.com uses on its CTA buttons: base #2D6A4F ("k-green") with
// a darker #245A42 ("k-green-dark") on hover. k-green is DARK, so the button label
// is WHITE (the previous FreeAgent green was light and used near-black text).
//
// Everything else (spacing, radii, focus rings, component internals) is left to
// Aura. NB: only the action steps of the primary ramp (500/600/700) are Kontala
// green; the lighter/darker tints (50–400, 800–950) are still the old set — they
// only feed subtle backgrounds, not buttons. Regenerate them if those need to match.
const FreeAgentPreset = definePreset(Aura, {
  semantic: {
    primary: {
      50: '#eef9eb',
      100: '#d6f0cf',
      200: '#bce6b1',
      300: '#a0db90',
      400: '#8ad27c',
      500: '#2d6a4f', // Kontala "k-green" — primary button background
      600: '#245a42', // Kontala "k-green-dark" — button hover
      700: '#1f4d38', // a touch darker — button active/pressed
      800: '#3f8038',
      900: '#2e5e29',
      950: '#1b3a18',
    },
    colorScheme: {
      light: {
        primary: {
          color: '{primary.500}',
          contrastColor: '#ffffff', // white label — k-green is dark (Kontala's CTAs use white text)
          hoverColor: '{primary.600}',
          activeColor: '{primary.700}',
        },
      },
    },
    // Tighter vertical spacing for dropdown rows (Aura's default is 0.5rem
    // top/bottom). Set on the shared SEMANTIC tokens so the density is
    // consistent app-wide from one place — no per-view CSS:
    //   - navigation.* drives the <Menu> popups (the account dropdown in
    //     AppTopBar, the "More" menu) — NOT the hand-rolled navy top nav bar,
    //     which is plain Tailwind, not a PrimeVue component.
    //   - list.* drives the <Select>/<MultiSelect> option lists used across
    //     the form views (Category, Status, Contact, period filters, …).
    // Only `padding` changes (definePreset deep-merges); horizontal 0.75rem and
    // everything else is left to Aura.
    navigation: {
      item: { padding: '0.3rem 0.75rem' },
    },
    list: {
      option: { padding: '0.3rem 0.75rem' },
    },
    // Slimmer form inputs. Aura pads fields more than the older v3 default theme,
    // which made every input look "fat" (42px tall). paddingY is the shared token
    // EVERY form control reads ({form.field.padding.y}) — InputText, Select,
    // Textarea, Password, InputNumber, DatePicker, … — so one value keeps them all
    // consistent and the same height. Only the vertical padding shrinks; font size
    // (16px), horizontal padding and the border/focus ring are left to Aura.
    formField: {
      paddingY: '0.375rem', // was 0.5rem → input height 42px → ~38px
    },
  },
})

export default FreeAgentPreset
