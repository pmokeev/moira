package checker

import "github.com/moira-alert/moira"

// threshold tracks a single WarnFor/ErrorFor timer: its configuration (forDuration,
// keepFiringFor) together with the bookkeeping that must be persisted between checks
// (MetricState's WarnSince/WarnRecoverSince or ErrorSince/ErrorRecoverSince).
type threshold struct {
	// forDuration is how many seconds the metric must continuously satisfy the threshold before
	// it fires.
	forDuration int64
	// keepFiringFor is how many seconds to keep reporting the threshold as fired after the metric
	// stops satisfying it. Zero means resolve immediately once the metric recovers.
	keepFiringFor int64
	// since is the unix timestamp when the metric started continuously satisfying the threshold,
	// or zero if it currently isn't.
	since int64
	// recoverSince is the unix timestamp when the metric first stopped satisfying an
	// already-fired threshold, or zero if no recovery grace period is running.
	recoverSince int64
}

// isFired reports whether the threshold has been continuously satisfied for at least forDuration.
func (t threshold) isFired(timestamp int64) bool {
	return t.since != 0 && timestamp-t.since >= t.forDuration
}

// advance moves the threshold one check step forward and returns the updated threshold:
//
//   - While condition is true, the timer starts (if it wasn't running) or keeps ticking towards
//     forDuration, and any in-progress keepFiringFor grace period is cancelled.
//   - While condition is false and the threshold hasn't fired yet, it resets immediately - there
//     is no grace period before firing.
//   - While condition is false and the threshold has already fired, it keeps reporting fired for
//     up to keepFiringFor seconds before resolving.
func (t threshold) advance(condition bool, timestamp int64) threshold {
	if condition {
		// Start the timer on the first satisfying tick, otherwise let it keep ticking.
		if t.since == 0 {
			t.since = timestamp
		}

		// Condition holds again, so cancel any grace period that was counting down.
		t.recoverSince = 0

		return t
	}

	if t.since == 0 {
		// Already inactive, nothing to advance.
		return t
	}

	if !t.isFired(timestamp) {
		// Never fired, so it resets immediately - no grace period applies.
		t.since = 0
		t.recoverSince = 0

		return t
	}

	// Already fired: anchor the recovery grace period on the first tick the condition drops.
	if t.recoverSince == 0 {
		t.recoverSince = timestamp
	}

	// Keep reporting fired until keepFiringFor seconds have passed (or none is configured).
	graceElapsed := t.keepFiringFor <= 0 || timestamp-t.recoverSince >= t.keepFiringFor
	if graceElapsed {
		t.since = 0
		t.recoverSince = 0
	}

	return t
}

// evaluateThresholds implements the WarnFor/ErrorFor/WarnKeepFiringFor/ErrorKeepFiringFor
// semantics. It must only be called with a raw state of OK, WARN or ERROR;
// NODATA/EXCEPTION are handled by the caller, which resets
// the timer fields instead of calling this function.
//
// It returns the effective state for this step (ERROR if errorThreshold is fired, else WARN if
// warnThreshold is fired, else OK) alongside the updated warn/error thresholds to persist back
// onto MetricState.
//
// For a severity whose For/KeepFiringFor are both zero (the feature isn't configured for it),
// its threshold is left zero-valued instead of being advanced: mathematically that would
// fire on the very same tick the raw condition becomes true anyway, so skipping it only avoids
// persisting throwaway since/recoverSince churn on MetricState for triggers that don't use it.
func evaluateThresholds(
	trigger *moira.Trigger,
	rawState moira.State,
	timestamp int64,
	prev moira.MetricState,
) (state moira.State, warnThreshold, errorThreshold threshold) {
	isWarnFired := rawState == moira.StateWARN || rawState == moira.StateERROR
	if trigger.WarnFor != 0 || trigger.WarnKeepFiringFor != 0 {
		warnThreshold = threshold{
			forDuration:   trigger.WarnFor,
			keepFiringFor: trigger.WarnKeepFiringFor,
			since:         prev.WarnSince,
			recoverSince:  prev.WarnRecoverSince,
		}.advance(isWarnFired, timestamp)

		isWarnFired = warnThreshold.isFired(timestamp)
	}

	isErrorFired := rawState == moira.StateERROR
	if trigger.ErrorFor != 0 || trigger.ErrorKeepFiringFor != 0 {
		errorThreshold = threshold{
			forDuration:   trigger.ErrorFor,
			keepFiringFor: trigger.ErrorKeepFiringFor,
			since:         prev.ErrorSince,
			recoverSince:  prev.ErrorRecoverSince,
		}.advance(isErrorFired, timestamp)

		isErrorFired = errorThreshold.isFired(timestamp)
	}

	state = moira.StateOK

	switch {
	case isErrorFired:
		state = moira.StateERROR
	case isWarnFired:
		state = moira.StateWARN
	}

	return state, warnThreshold, errorThreshold
}
