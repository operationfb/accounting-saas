<script setup lang="ts">
// Login screen — standalone full-page (no app chrome), modelled on FreeAgent's
// login. STATIC: nothing is wired up. The local refs below only exist so the
// PrimeVue inputs render; there is no submit/auth logic yet.
import { ref } from 'vue'
import InputText from 'primevue/inputtext'
import Checkbox from 'primevue/checkbox'
import Button from 'primevue/button'

const email = ref('')
const password = ref('')
const keepLoggedIn = ref(false)
</script>

<template>
  <div class="login">
    <div class="login__wordmark">Accounting</div>

    <div class="login__card">
      <h1 class="login__title">Ready to take care of business?</h1>

      <form class="login__form" @submit.prevent>
        <label class="login__label" for="email">Email address</label>
        <InputText id="email" v-model="email" class="w-full" autocomplete="username" />

        <label class="login__label" for="password">Password</label>
        <div class="login__password">
          <InputText
            id="password"
            v-model="password"
            type="password"
            class="w-full"
            autocomplete="current-password"
          />
          <button type="button" class="login__show">Show</button>
        </div>

        <div class="login__options">
          <label class="login__keep">
            <Checkbox v-model="keepLoggedIn" binary input-id="keep" />
            <span>Keep me logged in</span>
          </label>
          <span class="login__dot">•</span>
          <a href="#" class="fa-link">Reset my password</a>
        </div>

        <Button label="Log in" class="login__submit w-full" />
      </form>
    </div>

    <button type="button" class="login__google">
      <span class="login__google-g">G</span>
      Sign in with Google
    </button>
  </div>
</template>

<style scoped>
.login {
  min-height: 100%;
  background: var(--fa-bg);
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 56px 16px 40px;
  gap: 22px;
}
.login__wordmark {
  font-size: 34px;
  font-weight: 800;
  font-style: italic;
  color: var(--fa-blue);
  letter-spacing: -0.5px;
}
.login__card {
  width: 100%;
  max-width: 380px;
  background: #fff;
  border-radius: 6px;
  box-shadow: 0 1px 3px rgba(20, 40, 80, 0.12);
  padding: 28px;
}
.login__title {
  margin: 0 0 22px;
  font-size: 20px;
  font-weight: 700;
}
.login__label {
  display: block;
  margin: 14px 0 6px;
  font-size: 14px;
}
.login__form > .login__label:first-of-type {
  margin-top: 0;
}
.login__password {
  position: relative;
}
.login__show {
  position: absolute;
  top: 0;
  right: 0;
  height: 100%;
  padding: 0 14px;
  border: 0;
  background: transparent;
  color: var(--fa-blue);
  font-size: 14px;
  font-weight: 600;
  cursor: pointer;
  border-left: 1px solid var(--fa-input-border);
}
.login__options {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 16px 0 20px;
  font-size: 14px;
}
.login__keep {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}
.login__dot {
  color: var(--fa-muted);
}
.login__submit {
  justify-content: center;
  font-weight: 600;
}
.login__google {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  padding: 10px 18px;
  background: #fff;
  border: 1px solid var(--fa-border);
  border-radius: 4px;
  font-size: 14px;
  font-weight: 600;
  color: #3c4043;
  cursor: pointer;
}
.login__google-g {
  font-weight: 800;
  color: #4285f4;
}
</style>
