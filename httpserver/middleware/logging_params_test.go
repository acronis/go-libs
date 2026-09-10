/*
Copyright © 2024 Acronis International GmbH.

Released under MIT license.
*/

package middleware

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/ssgreg/logf"
	"github.com/stretchr/testify/require"

	"github.com/acronis/go-appkit/log"
	"github.com/acronis/go-appkit/log/logtest"
)

func TestLoggingParams_SetTimeSlotDurationMs(t *testing.T) {
	lp := LoggingParams{}

	lp.AddTimeSlotInt("slot1", 100)
	lp.AddTimeSlotInt("slot2", 200)
	lp.fields = append(lp.fields, log.Field{Key: "time_slots", Type: logf.FieldTypeObject, Any: lp.getTimeSlots()})

	expected := LoggingParams{
		fields: []log.Field{
			{
				Key:  "time_slots",
				Type: logf.FieldTypeObject,
				Any: loggableIntMap{
					"slot1": 100 * time.Millisecond,
					"slot2": 200 * time.Millisecond,
				},
			},
		},
	}

	require.ElementsMatch(t, lp.fields, expected.fields)
}

func TestLoggingParams_SetTimeSlotDuration(t *testing.T) {
	lp := LoggingParams{}

	lp.AddTimeSlotDurationInMs("slot1", 1*time.Second)
	lp.AddTimeSlotDurationInMs("slot2", 2*time.Second)
	lp.fields = append(lp.fields, log.Field{Key: "time_slots", Type: logf.FieldTypeObject, Any: lp.getTimeSlots()})

	expected := LoggingParams{
		fields: []log.Field{
			{
				Key:  "time_slots",
				Type: logf.FieldTypeObject,
				Any: loggableIntMap{
					"slot1": 1 * time.Second,
					"slot2": 2 * time.Second,
				},
			},
		},
	}
	require.ElementsMatch(t, lp.fields, expected.fields)
}

func TestLoggingParams_ExcludedDuration(t *testing.T) {
	t.Run("no time slots at all", func(t *testing.T) {
		lp := LoggingParams{}
		require.Zero(t, lp.excludedDuration())
	})

	t.Run("only regular time slots", func(t *testing.T) {
		lp := LoggingParams{}
		lp.AddTimeSlotInt("slot1", 100)
		lp.AddTimeSlotDurationInMs("slot2", 2*time.Second)
		require.Zero(t, lp.excludedDuration())
	})

	t.Run("excluded time slots are added to time_slots map too", func(t *testing.T) {
		lp := LoggingParams{}
		lp.AddTimeSlotInt("slot1", 100)
		lp.AddExcludedTimeSlotInt("slot2", 200)
		lp.AddExcludedTimeSlotDurationInMs("slot3", 300*time.Millisecond)

		require.Equal(t, loggableIntMap{
			"slot1": 100 * time.Millisecond,
			"slot2": 200 * time.Millisecond,
			"slot3": 300 * time.Millisecond,
		}, lp.getTimeSlots())
		require.Equal(t, 500*time.Millisecond, lp.excludedDuration())
	})

	t.Run("sub-millisecond slots are not truncated away", func(t *testing.T) {
		lp := LoggingParams{}
		for i := 0; i < 2000; i++ {
			lp.AddExcludedTimeSlotDurationInMs("slot", 900*time.Microsecond)
		}
		require.Equal(t, 1800*time.Millisecond, lp.excludedDuration())
		require.Equal(t, loggableIntMap{"slot": 1800 * time.Millisecond}, lp.getTimeSlots())
	})

	t.Run("same slot added as excluded and not", func(t *testing.T) {
		lp := LoggingParams{}
		lp.AddTimeSlotInt("slot1", 100)
		lp.AddExcludedTimeSlotInt("slot1", 200)

		require.Equal(t, loggableIntMap{"slot1": 300 * time.Millisecond}, lp.getTimeSlots())
		require.Equal(t, 200*time.Millisecond, lp.excludedDuration())
	})

	t.Run("concurrent adding", func(t *testing.T) {
		const goroutines = 32
		lp := LoggingParams{}
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				lp.AddTimeSlotInt("slot", 1)
				lp.AddExcludedTimeSlotInt("excluded_slot", 1)
			}()
		}
		wg.Wait()

		require.Equal(t, loggableIntMap{
			"slot":          goroutines * time.Millisecond,
			"excluded_slot": goroutines * time.Millisecond,
		}, lp.getTimeSlots())
		require.Equal(t, goroutines*time.Millisecond, lp.excludedDuration())
	})
}

func TestLoggableIntMap_EncodeLogfObject(t *testing.T) {
	var buf bytes.Buffer
	logger := logtest.NewLoggerWithOpts(logtest.LoggerOpts{Output: &buf})
	logger.Info("test", log.Field{Key: "time_slots", Type: logf.FieldTypeObject, Any: loggableIntMap{
		"slot1": 1500 * time.Millisecond,
		"slot2": 900 * time.Microsecond, // Still truncated on encoding, as before.
	}})

	require.Contains(t, buf.String(), `"slot1":1500`)
	require.Contains(t, buf.String(), `"slot2":0`)
}
