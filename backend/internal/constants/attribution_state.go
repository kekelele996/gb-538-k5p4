package constants

type AttributionState string

const (
	AttributionQueued      AttributionState = "queued"
	AttributionCalculating AttributionState = "calculating"
	AttributionCompleted   AttributionState = "completed"
	AttributionFailed      AttributionState = "failed"
	AttributionReviewed    AttributionState = "reviewed"
	AttributionConfirmed   AttributionState = "confirmed"
	AttributionVoided      AttributionState = "voided"
	AttributionInvalidated AttributionState = "invalidated"
)

// AttributionInvalidationSourceStates 列出可能被冻结输入变化级联失效的未确认状态。
var AttributionInvalidationSourceStates = []AttributionState{AttributionCompleted, AttributionReviewed}

var AttributionTransitions = map[AttributionState]map[AttributionState]bool{
	AttributionQueued:      {AttributionCalculating: true},
	AttributionCalculating: {AttributionCompleted: true, AttributionFailed: true},
	AttributionCompleted:   {AttributionReviewed: true, AttributionVoided: true},
	AttributionReviewed:    {AttributionConfirmed: true, AttributionVoided: true},
}

func CanTransitionAttribution(from, to AttributionState) bool {
	return AttributionTransitions[from][to]
}

// CanInvalidateAttribution 只有尚未确认且仍代表当前冻结输入的运行才允许被系统级联失效。
// 已确认结果保持不变；作废、失败与已失效运行不参与失效闭环。
func CanInvalidateAttribution(state AttributionState) bool {
	for _, candidate := range AttributionInvalidationSourceStates {
		if state == candidate {
			return true
		}
	}
	return false
}
