import { computed } from 'vue'
import { useAttributionRunStore } from '../stores/attribution-run-store'
import { useAuthStore } from '../stores/auth-store'

export function useAttributionRun() {
  const store = useAttributionRunStore()
  const auth = useAuthStore()
  const reviewerRole = ['admin', 'reviewer'].includes(auth.user?.role ?? '')
  // 已失效运行不允许复核或确认（后端同样返回 409，按钮直接隐藏避免误操作）。
  const canReviewSelected = computed(() =>
    store.selected?.attribution_state === 'completed' && reviewerRole)
  const canConfirmSelected = computed(() =>
    store.selected?.attribution_state === 'reviewed' && reviewerRole && store.selected?.created_by !== auth.user?.id)
  return { store, canReviewSelected, canConfirmSelected }
}
