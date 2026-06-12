import { createApp } from 'vue'
import { createPinia } from 'pinia'
import PrimeVue from 'primevue/config'

import './style.css'
import 'primeicons/primeicons.css'

import App from './App.vue'
import router from './router'
import FreeAgentPreset from './theme/preset'

const app = createApp(App)

app.use(createPinia())
app.use(router)
app.use(PrimeVue, {
  theme: {
    preset: FreeAgentPreset,
    options: {
      // FreeAgent is a light UI — pin to light mode by pointing the dark
      // selector at a class we never add, so the OS dark preference can't flip it.
      darkModeSelector: '.fa-dark',
      // Put PrimeVue styles in their own cascade layer so Tailwind utilities
      // can override them without !important.
      cssLayer: {
        name: 'primevue',
        order: 'theme, base, primevue',
      },
    },
  },
})

app.mount('#app')
