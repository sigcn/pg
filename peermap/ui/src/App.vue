<script setup>
import SigninView from './views/Signin.vue'
import MainView from './views/Main.vue'
import { ref, onMounted } from 'vue'
import { onBeforeMount } from 'vue'

const session = ref()

onBeforeMount(() => {
  let sessionVal = window.localStorage.getItem('session')
  session.value = JSON.parse(sessionVal)
})
</script>

<template>
  <SigninView v-if="!session" />
  <MainView v-else />
</template>
