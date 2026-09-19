package constants

import "testing"

func TestMeasurementStateMachine(t *testing.T) {
	if !CanTransitionMeasurement(MeasurementCaptured, MeasurementValidated) {
		t.Fatal("captured -> validated must be legal")
	}
	if CanTransitionMeasurement(MeasurementCaptured, MeasurementReady) {
		t.Fatal("captured -> ready must be rejected")
	}
	if !CanTransitionMeasurement(MeasurementReady, MeasurementSuperseded) {
		t.Fatal("ready -> superseded must be legal")
	}
}

func TestAttributionStateMachine(t *testing.T) {
	if !CanTransitionAttribution(AttributionCompleted, AttributionReviewed) {
		t.Fatal("completed -> reviewed must be legal")
	}
	if CanTransitionAttribution(AttributionCompleted, AttributionConfirmed) {
		t.Fatal("completed -> confirmed must require independent review")
	}
	if CanTransitionAttribution(AttributionConfirmed, AttributionVoided) {
		t.Fatal("confirmed result must remain immutable")
	}
}

func TestInvalidationEligibility(t *testing.T) {
	invalidable := []AttributionState{AttributionCompleted, AttributionReviewed}
	for _, state := range invalidable {
		if !CanInvalidateAttribution(state) {
			t.Fatalf("%s runs must be eligible for frozen-input invalidation", state)
		}
	}
	protected := []AttributionState{
		AttributionQueued, AttributionCalculating, AttributionFailed,
		AttributionConfirmed, AttributionVoided, AttributionInvalidated,
	}
	for _, state := range protected {
		if CanInvalidateAttribution(state) {
			t.Fatalf("%s runs must never be cascade-invalidated", state)
		}
	}
	// invalidated 是系统终态，不允许复核/确认/作废等任何人工迁移。
	if CanTransitionAttribution(AttributionInvalidated, AttributionReviewed) ||
		CanTransitionAttribution(AttributionInvalidated, AttributionConfirmed) ||
		CanTransitionAttribution(AttributionInvalidated, AttributionVoided) {
		t.Fatal("invalidated must be a terminal state for manual transitions")
	}
	for _, code := range []InvalidationReasonCode{
		InvalidationReasonPointCoordinates, InvalidationReasonPointBackground,
		InvalidationReasonPointInputsChanged, InvalidationReasonPointDeactivated,
		InvalidationReasonMeasurementReplaced, InvalidationReasonSourceRetired,
	} {
		if InvalidationReasonMessage(code) == "" {
			t.Fatalf("invalidation reason %s must carry a human-readable message", code)
		}
	}
}
