import { createRouter, createWebHashHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import RegisterView from './views/RegisterView.vue'
import AccountsView from './views/AccountsView.vue'
import ProxyPoolView from './views/ProxyPoolView.vue'
import SettingsView from './views/SettingsView.vue'
import EduView from './views/EduView.vue'
import IcloudView from './views/IcloudView.vue'
import PluginsView from './views/PluginsView.vue'
import WarpView from './views/WarpView.vue'
import DockerView from './views/DockerView.vue'
import UsageGuideView from './views/UsageGuideView.vue'

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', component: HomeView },
    { path: '/register', component: RegisterView },
    { path: '/accounts', component: AccountsView },
    { path: '/proxy-pool', component: ProxyPoolView },
    { path: '/warp', component: WarpView },
    { path: '/settings', component: SettingsView },
    { path: '/edu', component: EduView },
    { path: '/icloud', component: IcloudView },
    { path: '/plugins', component: PluginsView },
    { path: '/docker', component: DockerView },
    { path: '/guide', component: UsageGuideView }
  ]
})
