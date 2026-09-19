export type InvalidationReasonCode =
  | 'point_coordinates_changed'
  | 'point_background_changed'
  | 'point_inputs_changed'
  | 'point_deactivated'
  | 'measurement_superseded'
  | 'source_profile_retired'

export const invalidationReasonLabels: Record<InvalidationReasonCode, string> = {
  point_coordinates_changed: '监测点坐标更新',
  point_background_changed: '监测点背景谱更新',
  point_inputs_changed: '监测点坐标与背景谱更新',
  point_deactivated: '监测点停用',
  measurement_superseded: '冻结测量被替代',
  source_profile_retired: '冻结声源谱停用',
}

export const invalidationTriggerTypeLabels: Record<string, string> = {
  MonitoringPoint: '监测点',
  NoiseMeasurement: '噪声测量',
  SourceProfile: '声源谱',
}
