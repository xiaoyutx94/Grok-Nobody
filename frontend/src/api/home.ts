import { api, unwrap } from './client'

export const getMonitor = async () => unwrap(await api.get('/api/v1/admin/system/monitor'))
export const getCapabilities = async () => unwrap(await api.get('/api/v1/admin/system/capabilities'))
export const getRegisterStats = async () => unwrap(await api.get('/api/v1/admin/register-stats'))
export const listWarp = async () => unwrap<any[]>(await api.get('/api/v1/admin/warp/instances'))
export const getWarpLicense = async () => unwrap<{ license_key: string; has_license: boolean }>(await api.get('/api/v1/admin/warp/license'))
export const saveWarpLicense = async (license_key: string) => unwrap(await api.put('/api/v1/admin/warp/license', { license_key }))
export const getWarpKeys = async (probe = false) => unwrap(await api.get('/api/v1/admin/warp/keys', { params: { probe: probe ? 1 : 0 } }))
export const saveWarpKeySelection = async (payload: { mode?: 'custom' | 'catalog' | 'free'; key?: string; license_key?: string; remember_custom?: boolean }) =>
  unwrap(await api.put('/api/v1/admin/warp/keys/selection', payload))
export const probeWarpKey = async (key: string) => unwrap(await api.post('/api/v1/admin/warp/keys/probe', { key }))
export const deployWarp = async (count = 1, license_key = '') => unwrap(await api.post('/api/v1/admin/warp/deploy', { count, license_key }))
export const removeWarp = async (name: string) => unwrap(await api.post('/api/v1/admin/warp/remove', { name }))
export const refreshWarp = async (name = 'all') => unwrap(await api.post('/api/v1/admin/warp/refresh', { name }))
export const uninstallDocker = async (remove_all_containers = false) =>
  unwrap(await api.post('/api/v1/admin/plugins/uninstall-docker', { remove_all_containers }))

export const cleanDockerInstallers = async (keep_one = false) =>
  unwrap(await api.post('/api/v1/admin/plugins/docker-clean-installers', { keep_one }))

export const restartWarpAll = async () => unwrap(await api.post('/api/v1/admin/warp/restart-all', {}))
export const syncWarpProxyPool = async () => unwrap(await api.post('/api/v1/admin/warp/sync-proxy-pool', {}))

export const getGlobalProxy = async () => unwrap(await api.get('/api/v1/admin/warp/global-proxy'))
export const saveGlobalProxy = async (body: any) => unwrap(await api.put('/api/v1/admin/warp/global-proxy', body))

export const updateWarpUpstream = async (
  name: string,
  body: { upstream_proxy_mode: string; upstream_proxy?: string; recreate?: boolean }
) => unwrap(await api.put(`/api/v1/admin/warp/instances/${encodeURIComponent(name)}/upstream`, body, { timeout: 180_000 }))
export const recreateWarpOne = async (name: string) =>
  unwrap(await api.post(`/api/v1/admin/warp/instances/${encodeURIComponent(name)}/recreate`, {}, { timeout: 180_000 }))
