import { api, unwrap } from './client'

export const listEdu = async () => unwrap(await api.get('/api/v1/admin/edu-email'))
export const upsertWorker = async (body: any) => unwrap(await api.post('/api/v1/admin/edu-email/workers', body))
export const deleteWorker = async (id: string) => unwrap(await api.delete(`/api/v1/admin/edu-email/workers/${id}`))
export const provision = async (body: any) =>
  unwrap(await api.post('/api/v1/admin/edu-email/provision', body, { timeout: 300_000 }))
export const provisionBatch = async (body: any) =>
  unwrap(await api.post('/api/v1/admin/edu-email/provision-batch', body, { timeout: 600_000 }))
export const listZones = async (body: any) =>
  unwrap(
    await api.post(
      '/api/v1/admin/edu-email/zones',
      { probe_email_routing: true, ...body },
      { timeout: 180_000 }
    )
  )
export const generateAddresses = async (body: any) =>
  unwrap(await api.post('/api/v1/admin/edu-email/generate-addresses', body))
export const eduDocs = async () => unwrap(await api.get('/api/v1/admin/edu-email/docs'))

// Permanent multi-account CF credentials (Application Support)
export const listCFAccounts = async () => unwrap(await api.get('/api/v1/admin/edu-email/cf-accounts'))
export const upsertCFAccount = async (body: any) =>
  unwrap(await api.post('/api/v1/admin/edu-email/cf-accounts', body))
export const deleteCFAccount = async (id: string) =>
  unwrap(await api.delete(`/api/v1/admin/edu-email/cf-accounts/${id}`))
export const activateCFAccount = async (id: string) =>
  unwrap(await api.post(`/api/v1/admin/edu-email/cf-accounts/${id}/activate`))
export const getCFAccountRaw = async (id: string) =>
  unwrap(await api.get(`/api/v1/admin/edu-email/cf-accounts/${id}/raw`))

export const importOpen = async (body: any) =>
  unwrap(await api.post('/api/v1/admin/edu-email/import-open', body, { timeout: 180_000 }))
export const importOpenBatch = async (body: any) =>
  unwrap(await api.post('/api/v1/admin/edu-email/import-open-batch', body, { timeout: 600_000 }))
