import { api, unwrap } from './client'
export const listPlugins = async () => unwrap(await api.get('/api/v1/admin/plugins'))
export const installPlugin = async (body: any) =>
  unwrap(await api.post('/api/v1/admin/plugins/install', body, { timeout: 180_000 }))
export const stopPlugin = async (id: string) => unwrap(await api.post('/api/v1/admin/plugins/stop', { id }))
export const ensureDocker = async () =>
  unwrap(await api.post('/api/v1/admin/plugins/ensure-docker', {}, { timeout: 300_000 }))
// 异步 Docker 安装（带进度，轮询 dockerTask 展示）
export const dockerInstall = async () =>
  unwrap(await api.post('/api/v1/admin/plugins/docker-install', {}, { timeout: 10_000 }))
// Docker 安装 / 打码容器部署的任务进度快照
export const dockerTask = async () => unwrap(await api.get('/api/v1/admin/plugins/docker-task'))
