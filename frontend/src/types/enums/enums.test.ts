import { describe, expect, it } from 'vitest'
import { attributionStateLabels } from './attribution-state'
import { invalidationReasonLabels, invalidationTriggerTypeLabels } from './invalidation'
import { measurementQualityLabels, measurementStateLabels, measurementTransitions } from './measurement-quality'

describe('shared domain enums', () => {
  it('keeps every required measurement quality and state label', () => {
    expect(Object.keys(measurementQualityLabels)).toEqual(['valid', 'contaminated', 'clipped', 'missing'])
    expect(Object.keys(measurementStateLabels)).toEqual(['captured', 'validated', 'normalized', 'ready', 'rejected', 'superseded'])
    expect(measurementTransitions.captured).not.toContain('ready')
  })

  it('keeps all immutable attribution lifecycle labels', () => {
    expect(Object.keys(attributionStateLabels)).toEqual(['queued', 'calculating', 'completed', 'failed', 'reviewed', 'confirmed', 'voided', 'invalidated'])
  })

  it('covers every frozen-input invalidation trigger with a localized label', () => {
    expect(Object.keys(invalidationReasonLabels).sort()).toEqual([
      'measurement_superseded', 'point_background_changed', 'point_coordinates_changed',
      'point_deactivated', 'point_inputs_changed', 'source_profile_retired',
    ])
    for (const label of Object.values(invalidationReasonLabels)) {
      expect(label.length).toBeGreaterThan(0)
    }
    expect(invalidationTriggerTypeLabels.MonitoringPoint).toBeTruthy()
    expect(invalidationTriggerTypeLabels.NoiseMeasurement).toBeTruthy()
    expect(invalidationTriggerTypeLabels.SourceProfile).toBeTruthy()
  })
})
