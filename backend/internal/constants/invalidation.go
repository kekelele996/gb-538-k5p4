package constants

// InvalidationReasonCode 描述冻结输入失效闭环中触发失效的具体原因。
type InvalidationReasonCode string

const (
	InvalidationReasonPointCoordinates    InvalidationReasonCode = "point_coordinates_changed"
	InvalidationReasonPointBackground     InvalidationReasonCode = "point_background_changed"
	InvalidationReasonPointInputsChanged  InvalidationReasonCode = "point_inputs_changed"
	InvalidationReasonPointDeactivated    InvalidationReasonCode = "point_deactivated"
	InvalidationReasonMeasurementReplaced InvalidationReasonCode = "measurement_superseded"
	InvalidationReasonSourceRetired       InvalidationReasonCode = "source_profile_retired"
)

// 触发实体类型，写入失效审计的 entity_type 元数据，便于按来源追溯完整链路。
const (
	InvalidationEntityPoint       = "MonitoringPoint"
	InvalidationEntityMeasurement = "NoiseMeasurement"
	InvalidationEntitySource      = "SourceProfile"
)

// InvalidationReasonMessage 返回失效原因的中文说明，随失效运行详情返回。
func InvalidationReasonMessage(code InvalidationReasonCode) string {
	switch code {
	case InvalidationReasonPointCoordinates:
		return "冻结的监测点坐标已更新，传播距离与方向性修正不再可复现"
	case InvalidationReasonPointBackground:
		return "冻结的监测点背景谱已更新，背景扣除与归一化输入不再可复现"
	case InvalidationReasonPointInputsChanged:
		return "冻结的监测点坐标与背景谱均已更新，传播距离、方向性修正与背景扣除不再可复现"
	case InvalidationReasonPointDeactivated:
		return "冻结输入所属监测点已停用"
	case InvalidationReasonMeasurementReplaced:
		return "冻结测量已被替代（状态迁移为 superseded）"
	case InvalidationReasonSourceRetired:
		return "冻结声源谱版本已停用（废止）"
	default:
		return "冻结输入已变化，归因结果失效"
	}
}
