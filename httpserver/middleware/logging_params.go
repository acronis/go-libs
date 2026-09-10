/*
Copyright © 2024 Acronis International GmbH.

Released under MIT license.
*/

package middleware

import (
	"sync"
	"time"

	"github.com/ssgreg/logf"

	"github.com/acronis/go-appkit/log"
)

// loggableIntMap stores durations at full precision, but logs them as integer milliseconds.
type loggableIntMap map[string]time.Duration

func (lm loggableIntMap) EncodeLogfObject(e logf.FieldEncoder) error {
	for key, value := range lm {
		e.EncodeFieldInt64(key, value.Milliseconds())
	}

	return nil
}

// LoggingParams stores parameters for the Logging middleware
// that may be modified dynamically by the other underlying middlewares/handlers.
type LoggingParams struct {
	fields      []log.Field
	excluded    time.Duration
	timeSlots   loggableIntMap
	timeSlotsMu sync.RWMutex
}

// ExtendFields extends list of fields that will be logged by the Logging middleware.
func (lp *LoggingParams) ExtendFields(fields ...log.Field) {
	lp.fields = append(lp.fields, fields...)
}

// AddTimeSlotInt sets (if new) or adds duration value to the element of the time_slots map
func (lp *LoggingParams) AddTimeSlotInt(name string, dur int64) {
	lp.setIntMapFieldValue(name, time.Duration(dur)*time.Millisecond, false)
}

// AddTimeSlotDurationInMs sets (if new) or adds duration value in milliseconds to the element of the time_slots map
func (lp *LoggingParams) AddTimeSlotDurationInMs(name string, dur time.Duration) {
	lp.setIntMapFieldValue(name, dur, false)
}

// AddExcludedTimeSlotInt sets (if new) or adds duration value to the element of the time_slots map.
// The value is also excluded from the effective request duration used by the Logging middleware,
// so it doesn't count towards LoggingOpts.SlowRequestThreshold and LoggingOpts.TimeSlotsThreshold.
// The total request duration reported in the "duration_ms" field is not affected.
func (lp *LoggingParams) AddExcludedTimeSlotInt(name string, dur int64) {
	lp.setIntMapFieldValue(name, time.Duration(dur)*time.Millisecond, true)
}

// AddExcludedTimeSlotDurationInMs sets (if new) or adds duration value in milliseconds
// to the element of the time_slots map.
// The value is also excluded from the effective request duration used by the Logging middleware,
// so it doesn't count towards LoggingOpts.SlowRequestThreshold and LoggingOpts.TimeSlotsThreshold.
// The total request duration reported in the "duration_ms" field is not affected.
func (lp *LoggingParams) AddExcludedTimeSlotDurationInMs(name string, dur time.Duration) {
	lp.setIntMapFieldValue(name, dur, true)
}

func (lp *LoggingParams) setIntMapFieldValue(fieldName string, value time.Duration, excluded bool) {
	lp.timeSlotsMu.Lock()
	defer lp.timeSlotsMu.Unlock()
	if lp.timeSlots == nil {
		lp.timeSlots = make(loggableIntMap, 1)
	}
	lp.timeSlots[fieldName] += value
	if excluded {
		lp.excluded += value
	}
}

func (lp *LoggingParams) getTimeSlots() loggableIntMap {
	lp.timeSlotsMu.RLock()
	defer lp.timeSlotsMu.RUnlock()
	return lp.timeSlots
}

// excludedDuration returns the total duration of the time slots that were added as excluded.
func (lp *LoggingParams) excludedDuration() time.Duration {
	lp.timeSlotsMu.RLock()
	defer lp.timeSlotsMu.RUnlock()
	return lp.excluded
}
