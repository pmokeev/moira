package checker

import (
	"testing"

	"github.com/moira-alert/moira"
	. "github.com/smartystreets/goconvey/convey"
)

func TestThresholdAdvance(t *testing.T) {
	Convey("Test threshold.advance", t, func() {
		Convey("Condition met", func() {
			Convey("Was inactive, anchors since to now", func() {
				result := threshold{forDuration: 10}.advance(true, 100)
				So(result.since, ShouldEqual, int64(100))
				So(result.recoverSince, ShouldEqual, int64(0))
				So(result.isFired(100), ShouldBeFalse)
			})

			Convey("Already ticking, keeps original anchor", func() {
				result := threshold{forDuration: 10, since: 100}.advance(true, 105)
				So(result.since, ShouldEqual, int64(100))
				So(result.recoverSince, ShouldEqual, int64(0))
				So(result.isFired(105), ShouldBeFalse)
			})

			Convey("forDuration elapsed, fires", func() {
				result := threshold{forDuration: 10, since: 100}.advance(true, 110)
				So(result.since, ShouldEqual, int64(100))
				So(result.recoverSince, ShouldEqual, int64(0))
				So(result.isFired(110), ShouldBeTrue)
			})

			Convey("forDuration = 0 fires the same tick it becomes true", func() {
				result := threshold{}.advance(true, 100)
				So(result.since, ShouldEqual, int64(100))
				So(result.isFired(100), ShouldBeTrue)
			})

			Convey("Cancels an in-progress keep_firing_for countdown", func() {
				result := threshold{
					forDuration: 10, keepFiringFor: 20, since: 100, recoverSince: 140,
				}.advance(true, 150)
				So(result.since, ShouldEqual, int64(100))
				So(result.recoverSince, ShouldEqual, int64(0))
				So(result.isFired(150), ShouldBeTrue)
			})
		})

		Convey("Condition not met", func() {
			Convey("Already inactive, no-op", func() {
				result := threshold{forDuration: 10}.advance(false, 100)
				So(result.since, ShouldEqual, int64(0))
				So(result.recoverSince, ShouldEqual, int64(0))
				So(result.isFired(100), ShouldBeFalse)
			})

			Convey("Before forDuration elapses resets immediately, no grace", func() {
				result := threshold{
					forDuration: 10, keepFiringFor: 100, since: 100,
				}.advance(false, 105)
				So(result.since, ShouldEqual, int64(0))
				So(result.recoverSince, ShouldEqual, int64(0))
				So(result.isFired(105), ShouldBeFalse)
			})

			Convey("After firing, no keepFiringFor configured resolves immediately", func() {
				result := threshold{forDuration: 10, since: 100}.advance(false, 111)
				So(result.since, ShouldEqual, int64(0))
				So(result.recoverSince, ShouldEqual, int64(0))
				So(result.isFired(111), ShouldBeFalse)
			})

			Convey("After firing, within keepFiringFor grace stays fired", func() {
				result := threshold{
					forDuration: 10, keepFiringFor: 20, since: 100,
				}.advance(false, 111)
				So(result.since, ShouldEqual, int64(100))
				So(result.recoverSince, ShouldEqual, int64(111))
				So(result.isFired(111), ShouldBeTrue)
			})

			Convey("Grace already ticking, keeps original recover anchor", func() {
				result := threshold{
					forDuration: 10, keepFiringFor: 20, since: 100, recoverSince: 111,
				}.advance(false, 120)
				So(result.since, ShouldEqual, int64(100))
				So(result.recoverSince, ShouldEqual, int64(111))
				So(result.isFired(120), ShouldBeTrue)
			})

			Convey("Grace elapsed, resolves", func() {
				result := threshold{
					forDuration: 10, keepFiringFor: 20, since: 100, recoverSince: 111,
				}.advance(false, 131)
				So(result.since, ShouldEqual, int64(0))
				So(result.recoverSince, ShouldEqual, int64(0))
				So(result.isFired(131), ShouldBeFalse)
			})
		})
	})
}

func TestEvaluateThresholds(t *testing.T) {
	Convey("Test evaluateThresholds", t, func() {
		warnValue, errorValue := 10.0, 20.0
		baseTrigger := func() *moira.Trigger {
			return &moira.Trigger{
				WarnValue:   &warnValue,
				ErrorValue:  &errorValue,
				TriggerType: moira.RisingTrigger,
			}
		}

		Convey("For = 0 / unset behaves exactly like instant firing/resolving, without persisting timer bookkeeping", func() {
			trigger := baseTrigger()

			state, warnThreshold, errorThreshold := evaluateThresholds(trigger, moira.StateWARN, 100, moira.MetricState{})
			So(state, ShouldResemble, moira.StateWARN)
			So(warnThreshold, ShouldResemble, threshold{})
			So(errorThreshold, ShouldResemble, threshold{})

			// Prior WarnSince is ignored too: with WarnFor/WarnKeepFiringFor both unset, the warn
			// threshold is never tracked at all, regardless of what MetricState carried over.
			state, warnThreshold, errorThreshold = evaluateThresholds(trigger, moira.StateERROR, 110, moira.MetricState{WarnSince: 100})
			So(state, ShouldResemble, moira.StateERROR)
			So(warnThreshold, ShouldResemble, threshold{})
			So(errorThreshold, ShouldResemble, threshold{})

			state, warnThreshold, errorThreshold = evaluateThresholds(trigger, moira.StateOK, 120, moira.MetricState{WarnSince: 100, ErrorSince: 110})
			So(state, ShouldResemble, moira.StateOK)
			So(warnThreshold, ShouldResemble, threshold{})
			So(errorThreshold, ShouldResemble, threshold{})
		})

		Convey("Motivating scenario: WarnFor=10s, ErrorFor=10s, 5s WARN band then 5s ERROR band", func() {
			trigger := baseTrigger()
			trigger.WarnFor = 10
			trigger.ErrorFor = 10

			// Base offset keeps every "since" timestamp non-zero, since 0 doubles as the
			// "not currently tracked" sentinel in the algorithm.
			const base int64 = 1000

			metricState := moira.MetricState{}

			// base+0..base+4: WARN band, WarnFor timer starts ticking, not fired yet.
			for now := base; now <= base+4; now++ {
				state, warnThreshold, errorThreshold := evaluateThresholds(trigger, moira.StateWARN, now, metricState)
				So(state, ShouldResemble, moira.StateOK)
				metricState.WarnSince, metricState.WarnRecoverSince = warnThreshold.since, warnThreshold.recoverSince
				metricState.ErrorSince, metricState.ErrorRecoverSince = errorThreshold.since, errorThreshold.recoverSince
			}
			So(metricState.WarnSince, ShouldEqual, base)

			// base+5..base+9: ERROR band. WarnFor keeps accumulating from the original base+0 anchor
			// (never reset by the escalation); ErrorFor starts its own timer at base+5.
			for now := base + 5; now <= base+9; now++ {
				state, warnThreshold, errorThreshold := evaluateThresholds(trigger, moira.StateERROR, now, metricState)
				So(state, ShouldResemble, moira.StateOK)
				metricState.WarnSince = warnThreshold.since
				metricState.ErrorSince = errorThreshold.since
			}
			So(metricState.WarnSince, ShouldEqual, base)
			So(metricState.ErrorSince, ShouldEqual, base+5)

			// base+10: WARN's timer (anchored at base+0) reaches exactly WarnFor=10s -> WARN fires.
			// ERROR's timer (anchored at base+5) has only accumulated 5s -> not fired yet.
			state, warnThreshold, errorThreshold := evaluateThresholds(trigger, moira.StateERROR, base+10, metricState)
			So(state, ShouldResemble, moira.StateWARN)
			metricState.WarnSince = warnThreshold.since
			metricState.ErrorSince = errorThreshold.since

			// base+11..base+14: still in ERROR band, ERROR timer keeps accumulating.
			for now := base + 11; now <= base+14; now++ {
				state, _, errorThreshold := evaluateThresholds(trigger, moira.StateERROR, now, metricState)
				So(state, ShouldResemble, moira.StateWARN)
				metricState.ErrorSince = errorThreshold.since
			}

			// base+15: ERROR's timer (anchored at base+5) reaches ErrorFor=10s -> escalates to ERROR.
			state, _, _ = evaluateThresholds(trigger, moira.StateERROR, base+15, metricState)
			So(state, ShouldResemble, moira.StateERROR)
		})

		Convey("Full reset when the metric returns to OK before its threshold's for elapses", func() {
			trigger := baseTrigger()
			trigger.WarnFor = 10

			state, warnThreshold, _ := evaluateThresholds(trigger, moira.StateWARN, 100, moira.MetricState{})
			So(state, ShouldResemble, moira.StateOK)

			state, warnThreshold, _ = evaluateThresholds(trigger, moira.StateOK, 105, moira.MetricState{
				WarnSince:        warnThreshold.since,
				WarnRecoverSince: warnThreshold.recoverSince,
			})
			So(state, ShouldResemble, moira.StateOK)
			So(warnThreshold.since, ShouldEqual, int64(0))
			So(warnThreshold.recoverSince, ShouldEqual, int64(0))
		})

		Convey("WarnKeepFiringFor: brief dip below threshold doesn't resolve within grace, resolves after", func() {
			trigger := baseTrigger()
			trigger.WarnKeepFiringFor = 20

			metricState := moira.MetricState{}
			state, warnThreshold, _ := evaluateThresholds(trigger, moira.StateWARN, 100, metricState)
			So(state, ShouldResemble, moira.StateWARN)
			metricState.WarnSince, metricState.WarnRecoverSince = warnThreshold.since, warnThreshold.recoverSince

			// dip below threshold, within grace
			state, warnThreshold, _ = evaluateThresholds(trigger, moira.StateOK, 110, metricState)
			So(state, ShouldResemble, moira.StateWARN)
			metricState.WarnSince, metricState.WarnRecoverSince = warnThreshold.since, warnThreshold.recoverSince

			// grace elapses (started at t=110, WarnKeepFiringFor=20 -> elapses at t=130)
			state, warnThreshold, _ = evaluateThresholds(trigger, moira.StateOK, 131, metricState)
			So(state, ShouldResemble, moira.StateOK)
			So(warnThreshold.since, ShouldEqual, int64(0))
			So(warnThreshold.recoverSince, ShouldEqual, int64(0))
		})

		Convey("ErrorKeepFiringFor: downgrade ERROR to WARN (not OK) once grace expires while WARN condition still true", func() {
			trigger := baseTrigger()
			trigger.ErrorKeepFiringFor = 20

			metricState := moira.MetricState{}
			state, warnThreshold, errorThreshold := evaluateThresholds(trigger, moira.StateERROR, 100, metricState)
			So(state, ShouldResemble, moira.StateERROR)
			metricState.WarnSince = warnThreshold.since
			metricState.ErrorSince, metricState.ErrorRecoverSince = errorThreshold.since, errorThreshold.recoverSince

			// drops to WARN band: WARN condition true, ERROR condition false -> ERROR grace starts.
			state, warnThreshold, errorThreshold = evaluateThresholds(trigger, moira.StateWARN, 110, metricState)
			So(state, ShouldResemble, moira.StateERROR) // still within ErrorKeepFiringFor grace
			metricState.WarnSince = warnThreshold.since
			metricState.ErrorSince, metricState.ErrorRecoverSince = errorThreshold.since, errorThreshold.recoverSince

			// grace elapses (started at t=110, ErrorKeepFiringFor=20 -> elapses at t=130) while WARN
			// condition is still true -> downgrades to WARN, not OK.
			state, _, errorThreshold = evaluateThresholds(trigger, moira.StateWARN, 131, metricState)
			So(state, ShouldResemble, moira.StateWARN)
			So(errorThreshold.since, ShouldEqual, int64(0))
			So(errorThreshold.recoverSince, ShouldEqual, int64(0))
		})
	})
}
