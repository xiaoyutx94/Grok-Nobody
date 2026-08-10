import axios from 'axios'

export const api = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '',
  timeout: 60000
})

export function unwrap<T = any>(res: any): T {
  const d = res?.data
  if (d && typeof d === 'object' && 'data' in d) return d.data as T
  return d as T
}
