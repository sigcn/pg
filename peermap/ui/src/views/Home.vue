<script setup>
import http from '@/http'
import { onMounted, ref } from 'vue'

const session = ref()
const peers = ref([])
const serverInfo = ref()

const loadPeers = async () => {
  let r = await http.get('/pg/apis/v1/admin/peers', { session: session.value })
  if (r.code != 0) {
    alert(r.msg)
    return
  }
  r.data.forEach((peer) => {
    peers.value.push(new URL(peer))
  })
}

const loadServerInfo = async () => {
  let r = await http.get('/pg/apis/v1/admin/server_info', { session: session.value })
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

onMounted(async () => {
  let sessionVal = window.localStorage.getItem('session')
  session.value = JSON.parse(sessionVal)
  loadPeers()
  loadServerInfo()
})
</script>
<template>
  <header v-if="session">
    <span>{{ session.network }}</span> <a href="javascript:;" @click="signout">Sign out</a>
  </header>
  <main>
    <ul v-if="peers.length > 0">
      <li v-for="(peer, index) in peers" :key="index">
        <div class="id">{{ peer.pathname || peer.host }}</div>
        <div class="nat">{{ peer.searchParams.get('nat') }}</div>
        <div class="host">{{ peer.searchParams.get('name') }}</div>
        <div class="ipv4">IPv4: {{ peer.searchParams.get('alias1') }}</div>
        <div class="ipv6">IPv6: {{ peer.searchParams.get('alias2') }}</div>
        <div class="addrs">Addrs: {{ peer.searchParams.get('addr') }}</div>
      </li>
    </ul>
    <div v-else class="usage">
      <div class="title">No any node found, run your first node with:</div>
      <div class="code">
        <code>pgcli vpn -s {{ (serverInfo || { url: '' }).url }} -4 100.99.0.1/24</code>
      </div>
    </div>
  </main>
  <footer v-if="serverInfo">
    <span
      >{{ serverInfo.version }}_{{ serverInfo.vcs_revision }}, build on
      {{ serverInfo.vcs_time }} using {{ serverInfo.go_version }}</span
    >
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
  font-size: 16px;
  font-weight: bold;
}
header a {
  font-size: 14px;
}

.usage {
  padding: 10px;
}

.usage .title {
  font-size: 18px;
}

ul {
  padding: 10px;
}

ul li {
  width: calc(100vw - 20px);
  display: inline-block;
  background-color: #f1f1f1;
  padding: 10px;
  border-radius: 5px;
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
}
ul li .nat {
  float: right;
  background-color: #69c;
  border-radius: 3px;
  padding: 0px 5px;
  color: #fff;
  font-size: 12px;
}
ul li .host {
  font-weight: bold;
}
footer {
  width: 100%;
  text-align: center;
  font-size: 12px;
  color: #999;
  padding: 30px 0;
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
