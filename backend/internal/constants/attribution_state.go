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
	// AttributionInvalidated is a system-controlled terminal state. It is only
	// reached through the frozen-input invalidation closed loop, never through a
	// client-requested transition, so no outgoing transition is registered.
	AttributionInvalidated AttributionState = "invalidated"
)

var AttributionTransitions = map[AttributionState]map[AttributionState]bool{
	AttributionQueued:      {AttributionCalculating: true},
	AttributionCalculating: {AttributionCompleted: true, AttributionFailed: true},
	AttributionCompleted:   {AttributionReviewed: true, AttributionVoided: true},
	AttributionReviewed:    {AttributionConfirmed: true, AttributionVoided: true},
}

// InvalidationUnconfirmedStates holds every run that still depends on its
// frozen inputs and therefore must be invalidated when those inputs change.
// Confirmed results are deliberately excluded; failed/voided runs are already
// terminal and carry no usable conclusion.
var InvalidationUnconfirmedStates = []string{
	string(AttributionCompleted), string(AttributionReviewed),
}

func CanTransitionAttribution(from, to AttributionState) bool {
	return AttributionTransitions[from][to]
}

// Frozen input invalidation trigger reasons. Stored on the invalidated run so
// the detail view can explain why the conclusion is no longer usable.
const (
	InvalidationReasonPointCoordinates      = "监测点坐标已更新，冻结传播距离与衰减矩阵失效"
	InvalidationReasonPointBackground       = "监测点背景谱已更新，冻结背景扣除与归一化频带失效"
	InvalidationReasonPointDeactivated      = "监测点已停用，冻结监测点不再作为有效归因受体"
	InvalidationReasonMeasurementSuperseded = "冻结测量已被替代（ready -> superseded），冻结输入不再代表当前测量"
	InvalidationReasonSourceRetired         = "冻结声源谱版本已废止（active -> retired），候选声源不再有效"
)

// Invalidation entity types identify which frozen entity triggered the loop.
const (
	InvalidationEntityMonitoringPoint  = "MonitoringPoint"
	InvalidationEntityNoiseMeasurement = "NoiseMeasurement"
	InvalidationEntitySourceProfile    = "SourceProfile"
)
