import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import './styles/main.css'
// 共享版式原语（代理池 / WARP / EDU / 插件 / 设置五个页面用）
import './styles/views.css'
import { installKeyboardShortcuts } from './utils/keyboard'

installKeyboardShortcuts()
createApp(App).use(router).mount('#app')
