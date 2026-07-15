/**
 * Axios HTTP Client Configuration
 * Base client with interceptors for authentication, token refresh, and error handling
 */

import axios, { AxiosInstance, AxiosError, InternalAxiosRequestConfig, AxiosResponse } from 'axios'
import type { ApiResponse } from '@/types'
import { getLocale } from '@/i18n'
import {
  clearSessionAccessToken,
  getBrowserSessionHeaders,
  getSessionAccessToken,
  setSessionAccessToken
} from '@/auth/browserSession'

// ==================== Axios Instance Configuration ====================

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL || '/api/v1'

export const apiClient: AxiosInstance = axios.create({
  baseURL: API_BASE_URL,
  withCredentials: true,
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json',
    ...getBrowserSessionHeaders()
  }
})

export interface BrowserSessionRefreshResponse {
  access_token: string
  refresh_token?: string
  expires_in: number
  token_type: string
}

let browserSessionRefreshPromise: Promise<BrowserSessionRefreshResponse> | null = null

async function rotateBrowserSessionToken(): Promise<BrowserSessionRefreshResponse> {
  const rotate = async () => {
    const { data } = await apiClient.post<BrowserSessionRefreshResponse>('/auth/refresh', {})
    if (!data || typeof data.access_token !== 'string' || !data.access_token.trim()) {
      throw new Error('Token refresh returned an invalid response')
    }
    setSessionAccessToken(data.access_token)
    return data
  }

  // The refresh credential is a one-time rotating cookie shared by all tabs.
  // Web Locks serialize tab-level rotations; the in-memory promise below handles
  // concurrency inside one tab. Older browsers retain the single-tab guarantee.
  if (typeof navigator !== 'undefined' && navigator.locks) {
    return navigator.locks.request('sub2api-browser-refresh', { mode: 'exclusive' }, rotate)
  }
  return rotate()
}

/**
 * Rotate the one-time refresh credential exactly once even when proactive refresh
 * and multiple 401 responses arrive together. Concurrent rotation would otherwise
 * trigger refresh-token reuse detection and revoke the successful descendant.
 */
export function refreshBrowserSessionToken(): Promise<BrowserSessionRefreshResponse> {
  if (browserSessionRefreshPromise) {
    return browserSessionRefreshPromise
  }
  browserSessionRefreshPromise = rotateBrowserSessionToken().finally(() => {
    browserSessionRefreshPromise = null
  })
  return browserSessionRefreshPromise
}

// ==================== Request Interceptor ====================

// Get user's timezone
const getUserTimezone = (): string => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone
  } catch {
    return 'UTC'
  }
}

apiClient.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    // Browser access tokens live only in memory. Refresh credentials are HttpOnly cookies.
    const token = getSessionAccessToken()
    if (token && config.headers) {
      config.headers.Authorization = `Bearer ${token}`
    }

    // Attach locale for backend translations
    if (config.headers) {
      config.headers['Accept-Language'] = getLocale()
      const browserSessionHeaders = getBrowserSessionHeaders()
      for (const [name, value] of Object.entries(browserSessionHeaders)) {
        config.headers[name] = value
      }
    }

    // Attach timezone for all GET requests (backend may use it for default date ranges)
    if (config.method === 'get') {
      if (!config.params) {
        config.params = {}
      }
      config.params.timezone = getUserTimezone()
    }

    return config
  },
  (error) => {
    return Promise.reject(error)
  }
)

// ==================== Response Interceptor ====================

apiClient.interceptors.response.use(
  (response: AxiosResponse) => {
    // Unwrap standard API response format { code, message, data }
    const apiResponse = response.data as ApiResponse<unknown>
    if (apiResponse && typeof apiResponse === 'object' && 'code' in apiResponse) {
      if (apiResponse.code === 0) {
        // Success - return the data portion
        response.data = apiResponse.data
      } else {
        // API error
        const resp = apiResponse as unknown as Record<string, unknown>
        return Promise.reject({
          status: response.status,
          code: apiResponse.code,
          message: apiResponse.message || 'Unknown error',
          reason: resp.reason,
          metadata: resp.metadata,
        })
      }
    }
    return response
  },
  async (error: AxiosError<ApiResponse<unknown>>) => {
    // Request cancellation: keep the original axios cancellation error so callers can ignore it.
    // Otherwise we'd misclassify it as a generic "network error".
    if (error.code === 'ERR_CANCELED' || axios.isCancel(error)) {
      return Promise.reject(error)
    }

    const originalRequest = error.config as InternalAxiosRequestConfig & { _retry?: boolean }

    // Handle common errors
    if (error.response) {
      const { status, data } = error.response
      const url = String(error.config?.url || '')

      // Validate `data` shape to avoid HTML error pages breaking our error handling.
      const apiData = (typeof data === 'object' && data !== null ? data : {}) as Record<string, any>

      // Ops monitoring disabled: treat as feature-flagged 404, and proactively redirect away
      // from ops pages to avoid broken UI states.
      if (status === 404 && apiData.message === 'Ops monitoring is disabled') {
        try {
          localStorage.setItem('ops_monitoring_enabled_cached', 'false')
        } catch {
          // ignore localStorage failures
        }
        try {
          window.dispatchEvent(new CustomEvent('ops-monitoring-disabled'))
        } catch {
          // ignore event failures
        }

        if (window.location.pathname.startsWith('/admin/ops')) {
          window.location.href = '/admin/settings'
        }

        return Promise.reject({
          status,
          code: 'OPS_DISABLED',
          message: apiData.message || error.message,
          url
        })
      }

      // 401: Try the HttpOnly-cookie refresh flow once.
      // This handles TOKEN_EXPIRED, INVALID_TOKEN, TOKEN_REVOKED, etc.
      if (status === 401 && !originalRequest._retry) {
        const isAuthEndpoint =
          url.includes('/auth/login') || url.includes('/auth/register') || url.includes('/auth/refresh')

        if (!isAuthEndpoint) {
          originalRequest._retry = true

          try {
            const refreshed = await refreshBrowserSessionToken()
            if (originalRequest.headers) {
              originalRequest.headers.Authorization = `Bearer ${refreshed.access_token}`
            }
            return apiClient(originalRequest)
          } catch {
            clearSessionAccessToken()
            sessionStorage.setItem('auth_expired', '1')

            if (!window.location.pathname.includes('/login')) {
              window.location.href = '/login'
            }

            return Promise.reject({
              status: 401,
              code: 'TOKEN_REFRESH_FAILED',
              message: 'Session expired. Please log in again.'
            })
          }
        }

        // Auth endpoint failure: clear the in-memory credential and redirect.
        const hasToken = !!getSessionAccessToken()
        const headers = error.config?.headers as Record<string, unknown> | undefined
        const authHeader = headers?.Authorization ?? headers?.authorization
        const sentAuth =
          typeof authHeader === 'string'
            ? authHeader.trim() !== ''
            : Array.isArray(authHeader)
              ? authHeader.length > 0
              : !!authHeader

        clearSessionAccessToken()
        if ((hasToken || sentAuth) && !isAuthEndpoint) {
          sessionStorage.setItem('auth_expired', '1')
          if (!window.location.pathname.includes('/login')) {
            window.location.href = '/login'
          }
        }
      }

      // Return structured error
      return Promise.reject({
        status,
        code: apiData.code,
        reason: apiData.reason,
        error: apiData.error,
        message: apiData.message || apiData.detail || error.message,
        metadata: apiData.metadata,
      })
    }

    // Network error
    return Promise.reject({
      status: 0,
      message: 'Network error. Please check your connection.'
    })
  }
)

export default apiClient
