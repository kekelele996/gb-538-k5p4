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
	if CanTransitionAttribution(AttributionInvalidated, AttributionReviewed) {
		t.Fatal("invalidated result must never return to review")
	}
	if CanTransitionAttribution(AttributionInvalidated, AttributionConfirmed) {
		t.Fatal("invalidated result must never be confirmed")
	}
	if CanTransitionAttribution(AttributionInvalidated, AttributionVoided) {
		t.Fatal("invalidated is a terminal state and must not be manually voided")
	}
}

func TestInvalidationOnlyTouchesUnconfirmedRuns(t *testing.T) {
	for _, state := range InvalidationUnconfirmedStates {
		if AttributionState(state) == AttributionConfirmed {
			t.Fatal("confirmed runs must never be invalidated")
		}
		if state != string(AttributionCompleted) && state != string(AttributionReviewed) {
			t.Fatalf("unexpected invalidation target state %q", state)
		}
	}
}
