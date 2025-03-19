<script setup>
import http from '@/http'
import { onMounted, ref } from 'vue'

const secret = ref()
const secretInput = ref()
const providers = ref()

const loadProviders = async () => {
  let r = await http.get('/oidc/providers')
  if (r.code != 0) {
    alert(r.msg)
    return
  }
  providers.value = r.data
}

const requestSecret = async (state) => {
  let r = await http.get(`/oidc/secret?state=${state}`)
  if (!r.network) {
    console.log(r.msg)
    return
  }
  window.localStorage.setItem('session', JSON.stringify(r))
  window.location.href = ''
}

const signinBtn = async () => {
  if (!secret.value || secret.value.length == 0) {
    secretInput.value.focus()
    return
  }
  try {
    JSON.parse(secret.value)
  } catch (e) {
    console.log(e)
    secretInput.value.focus()
    return
  }
  window.localStorage.setItem('session', secret.value)
  window.location.href = ''
}

const signin = (provider) => {
  let state = `PG_ADM${crypto.randomUUID()}`
  requestSecret(state)
  window.open(`/oidc/${provider}?state=${state}`, '_blank')
}

onMounted(loadProviders)
</script>
<template>
  <main>
    <div class="header">
      <div class="logo">
        <img src="@/assets/logo.png" />
        <span>PeerGuard</span>
      </div>
    </div>
    <div class="secret">
      <input ref="secretInput" v-model="secret" :placeholder="$t('enter_secret')" />
      <button @click="signinBtn">{{ $t('sign_in') }}</button>
    </div>
    <div v-if="providers" class="or">{{ $t('or') }}</div>
    <ul v-if="providers" class="login">
      <li v-for="(provider, index) in providers" :key="index" @click="signin(provider)">
        {{
          $t('sign_in_with', { provider: provider.replace(/^./, (match) => match.toUpperCase()) })
        }}
      </li>
    </ul>
    <div class="tips">
      <span style="color: #000">{{ $t('first_time') }}</span>
      {{ $t('read_docs') }} <a href="https://docs.openpg.in">docs.openpg.in</a>
    </div>
  </main>
</template>

<style scoped>
main {
  display: block;
  text-align: center;
}
.logo img {
  width: 32px;
  height: 32px;
  float: left;
  margin-right: 5px;
}
.logo {
  display: inline-block;
  line-height: 32px;
  font-size: 18px;
  user-select: none;
  margin: 30px 0 50px 0;
}
.logo span {
  font-weight: bold;
}
.login {
  padding: 0;
  margin: 0;
  display: inline-block;
  width: 320px;
}
.login li {
  list-style: none;
  width: 320px;
  border: 1px solid #dadada;
  border-radius: 3px;
  line-height: 38px;
  margin: 10px 0;
  box-shadow: 0 0 8px #f1f1f1;
}
.login li:hover {
  border-color: #ccc;
  cursor: pointer;
}

.secret {
  width: 320px;
  margin: 0 auto;
}

.secret input {
  line-height: 38px;
  width: 100%;
  border: 1px solid #dadada;
  border-radius: 3px;
  font-size: 16px;
  padding: 0 5px;
  background-color: #f1f1f1;
}
.secret button {
  margin-top: 10px;
  line-height: 38px;
  width: 100%;
  border-radius: 3px;
  font-size: 16px;
  border: none;
  background-color: #044941;
  color: #fff;
}
.secret button:hover {
  cursor: pointer;
  background-color: #03352f;
}
.or {
  width: 320px;
  margin: 20px auto;
  font-size: 12px;
}

.tips {
  font-size: 13px;
  margin-top: 30px;
}
</style>
