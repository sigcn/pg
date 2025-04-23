<script setup>
import http from '@/http'
import dayjs from 'dayjs'
import duration from 'dayjs/plugin/duration'
import relativeTime from 'dayjs/plugin/relativeTime'
import { onMounted, ref } from 'vue'

dayjs.extend(duration)
dayjs.extend(relativeTime)
const session = ref()
const peers = ref([])
const serverInfo = ref()

const loadPeers = async () => {
  let r = await http.get('/api/v1/r5/peers', { session: session.value })
  if (r.code != 0) {
    alert(r.msg)
    return
  }
  if (!r.data) {
    return
  }
  r.data.forEach((peer) => {
    peers.value.push(new URL(peer))
  })
}

const loadServerInfo = async () => {
  let r = await http.get('/api/v1/r5/server_info', { session: session.value })
  if (r.code != 0) {
    alert(r.msg)
    return
  }
  r.data.url = `${window.location.protocol}//${window.location.host}/pg`
  serverInfo.value = r.data
}

const signout = () => {
  window.localStorage.removeItem('session')
  window.location.href = ''
}

const downloadSecret = async () => {
  let r = await http.download('/api/v1/r5/psns.json', { session: session.value })
  if (r.code != 0) {
    alert(r.msg)
  }
}

onMounted(async () => {
  let sessionVal = window.localStorage.getItem('session')
  session.value = JSON.parse(sessionVal)
  loadPeers()
  loadServerInfo()
})
</script>
<template>
  <header v-if="session">
    <div class="network">
      <span>{{ session.network }}</span>
      <a href="javascript:;" @click="signout">{{ $t('sign_out') }}</a>
    </div>
    <a class="generateSecret" href="javascript:;" @click="downloadSecret">
      {{ $t('generate_secret') }}
    </a>
  </header>
  <main v-if="session">
    <ul v-if="peers.length > 0">
      <li v-for="(peer, index) in peers" :key="index">
        <div class="id">{{ (peer.pathname || peer.host).replace(/^\/\//, '') }}</div>
        <div class="meta">
          <span
            :class="{
              nat: en[0] == 'nat',
              name: en[0] == 'name',
              version: en[0] == 'version',
              st: en[0] == 'st',
            }"
            :title="
              en[0] == 'st'
                ? `${dayjs.unix(Number(en[1])).format('YYYY-MM-DD HH:mm:ss')}, ${dayjs
                    .duration(dayjs().unix() - Number(en[1]), 'seconds')
                    .humanize()}`
                : ''
            "
            v-for="(en, i) in peer.searchParams.entries()"
            :key="i"
          >
            {{ en[0] }}={{ en[1] }}
          </span>
        </div>
      </li>
    </ul>
    <div v-else class="usage">
      <div class="title">{{ $t('help.no_any_node') }}</div>
      <div class="code">
        <div class="step">{{ $t('help.step1') }}</div>
        <div class="stepc">
          <i18n-t keypath="help.step1c">
            <template #link>
              <a href="https://github.com/sigcn/pg/releases">releases</a>
            </template>
          </i18n-t>
        </div>
        <div class="step">{{ $t('help.step2') }}</div>
        <div class="stepc">
          <i18n-t keypath="help.step2c">
            <template #btn>
              <strong>{{ $t('generate_secret') }}</strong>
            </template>
          </i18n-t>
        </div>
        <div class="step">{{ $t('help.step3') }}</div>
        <code
          >pgcli vpn -s {{ (serverInfo || { url: '' }).url }} -4 100.99.0.1/24 -f
          {{ session.network }}_psns.json</code
        >
        <div class="title">
          <i18n-t keypath="help.read_docs">
            <template #link> <a href="https://docs.openpg.in">docs</a></template>
          </i18n-t>
        </div>
      </div>
    </div>
  </main>
  <footer v-if="serverInfo">
    <div>{{ serverInfo.version }}-{{ serverInfo.vcs_revision }}</div>
    <div>build on {{ serverInfo.vcs_time }} using {{ serverInfo.go_version }}</div>
  </footer>
</template>

<style scoped>
header {
  height: 32px;
  line-height: 32px;
  background-color: #fcfcfc;
  border-bottom: 1px solid #f0f0f0;
  padding: 0 10px;
}
header span {
  font-size: 18px;
  font-weight: bold;
}
header a {
  font-size: 14px;
  margin-left: 5px;
}

header .generateSecret {
  float: right;
  margin: -35px 0 0 0;
}

main {
  color: var(--vt-c-black);
  min-height: calc(100vh - 100px);
}

.usage {
  padding: 10px;
}

.usage .title {
  font-size: 18px;
  margin: 10px 0;
}

.usage .step {
  line-height: 32px;
  font-size: 14px;
  font-weight: bold;
}
.usage .stepc,
.usage code {
  color: var(--vt-c-text-light-1);
}
.usage .stepc strong {
  font-size: 16px;
  font-weight: bold;
}
ul {
  padding: 10px;
  width: 100%;
  display: flex;
  flex-wrap: wrap;
}

ul li {
  width: calc(100vw - 20px);
  display: inline-block;
  background-color: #f1f1f1;
  padding: 10px;
  border-radius: 3px;
  line-height: 22px;
  font-size: 14px;
  margin: 0 10px 10px 0;
}
ul li:hover {
  box-shadow: 0 0 10px #f0f0f0;
}
ul li .id {
  font-size: 16px;
  margin-bottom: 10px;
  overflow-x: scroll;
  font-weight: bold;
}

.meta span {
  display: inline-block;
  background-color: #e0e0e0;
  border-radius: 10px;
  padding: 0 10px;
  line-height: 22px;
  font-size: 13px;
  color: var(--color-text);
  margin: 0 5px 5px 0;
}

.meta .name {
  background-color: #181717;
  color: #fff;
}

.meta .nat {
  background: linear-gradient(90deg, #0df6a4, #19a3f3);
  color: #fff;
}

footer {
  width: 100%;
  text-align: center;
  font-size: 13px;
  color: #666;
  padding: 10px 0 0 0;
}

@media (min-width: 1024px) {
  ul li {
    width: 460px;
  }
  ul li .id {
    overflow-x: auto;
  }
}
</style>
