import { ref, reactive, onUnmounted, toRaw } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import type { BasePaginationResponse, FetchOptions } from '@/types'
import { getPersistedPageSize, setPersistedPageSize } from './usePersistedPageSize'

interface PaginationState {
  page: number
  page_size: number
  total: number
  pages: number
}

interface TableLoaderOptions<T, P> {
  fetchFn: (page: number, pageSize: number, params: P, options?: FetchOptions) => Promise<BasePaginationResponse<T>>
  initialParams?: P
  pageSize?: number
  debounceMs?: number
}

/**
 * 通用表格数据加载 Composable
 * 统一处理分页、筛选、搜索防抖和请求取消
 */
export function useTableLoader<T, P extends Record<string, any>>(options: TableLoaderOptions<T, P>) {
  const { fetchFn, initialParams, pageSize, debounceMs = 300 } = options

  const items = ref<T[]>([])
  const loading = ref(false)
  const params = reactive<P>({ ...(initialParams || {}) } as P)
  const pagination = reactive<PaginationState>({
    page: 1,
    page_size: pageSize ?? getPersistedPageSize(),
    total: 0,
    pages: 0
  })

  let abortController: AbortController | null = null
  let requestSeq = 0

  const isAbortError = (error: any) => {
    return error?.name === 'AbortError' || error?.code === 'ERR_CANCELED' || error?.name === 'CanceledError'
  }

  const load = async () => {
    const previousController = abortController
    const currentController = new AbortController()
    const requestId = ++requestSeq
    abortController = currentController
    previousController?.abort()
    loading.value = true

    const isCurrentRequest = () => requestId === requestSeq && abortController === currentController

    try {
      const response = await fetchFn(
        pagination.page,
        pagination.page_size,
        toRaw(params) as P,
        { signal: currentController.signal }
      )

      if (!isCurrentRequest()) return

      items.value = response.items || []
      pagination.total = response.total || 0
      pagination.pages = response.pages || 0
    } catch (error) {
      if (!isAbortError(error)) {
        if (isCurrentRequest()) {
          console.error('Table load error:', error)
          throw error
        }
      }
    } finally {
      if (isCurrentRequest()) {
        abortController = null
        loading.value = false
      }
    }
  }

  const reload = () => {
    pagination.page = 1
    return load()
  }

  const debouncedReload = useDebounceFn(reload, debounceMs)

  const handlePageChange = (page: number) => {
    // 确保页码在有效范围内
    const validPage = Math.max(1, Math.min(page, pagination.pages || 1))
    pagination.page = validPage
    load()
  }

  const handlePageSizeChange = (size: number) => {
    pagination.page_size = size
    pagination.page = 1
    setPersistedPageSize(size)
    load()
  }

  onUnmounted(() => {
    abortController?.abort()
  })

  return {
    items,
    loading,
    params,
    pagination,
    load,
    reload,
    debouncedReload,
    handlePageChange,
    handlePageSizeChange
  }
}
