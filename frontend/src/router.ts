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
import GatewayView from './views/GatewayView.vue'
import GatewayGroupsView from './views/GatewayGroupsView.vue'
import GatewayKeysView from './views/GatewayKeysView.vue'
import GatewayServicesView from './views/GatewayServicesView.vue'
import ChatView from './views/ChatView.vue'

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', component: HomeView },
    { path: '/register', component: RegisterView },
    { path: '/accounts', component: AccountsView },
    { path: '/chat', component: ChatView },
    { path: '/proxy-pool', component: ProxyPoolView },
    { path: '/warp', component: WarpView },
    { path: '/settings', component: SettingsView },
    { path: '/edu', component: EduView },
    { path: '/icloud', component: IcloudView },
    { path: '/plugins', component: PluginsView },
    { path: '/docker', component: DockerView },
    { path: '/guide', component: UsageGuideView },
    { path: '/gateway', component: GatewayView },
    { path: '/gateway/groups', component: GatewayGroupsView },
    { path: '/gateway/keys', component: GatewayKeysView },
    { path: '/gateway/services', component: GatewayServicesView }
  ]
})
