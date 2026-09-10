/*
Copyright © 2025 Acronis International GmbH.

Released under MIT license.
*/

package interceptor

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/ssgreg/logf"
	"github.com/stretchr/testify/suite"

	"github.com/acronis/go-appkit/log"
	"github.com/acronis/go-appkit/log/logtest"
)

// LoggingParamsTestSuite is a test suite for LoggingParams
type LoggingParamsTestSuite struct {
	suite.Suite
}

func TestLoggingParams(t *testing.T) {
	suite.Run(t, &LoggingParamsTestSuite{})
}

func (s *LoggingParamsTestSuite) TestExtendFields() {
	lp := &LoggingParams{}

	// Test adding fields
	lp.ExtendFields(
		log.String("field1", "value1"),
		log.Int("field2", 42),
		log.Bool("field3", true),
	)

	s.Require().Len(lp.fields, 3)
	s.Require().Equal("field1", lp.fields[0].Key)
	s.Require().Equal("value1", string(lp.fields[0].Bytes))
	s.Require().Equal("field2", lp.fields[1].Key)
	s.Require().Equal(int64(42), lp.fields[1].Int)
	s.Require().Equal("field3", lp.fields[2].Key)
	s.Require().True(lp.fields[2].Int != 0)

	// Test adding more fields
	lp.ExtendFields(log.String("field4", "value4"))
	s.Require().Len(lp.fields, 4)
	s.Require().Equal("field4", lp.fields[3].Key)
	s.Require().Equal("value4", string(lp.fields[3].Bytes))
}

func (s *LoggingParamsTestSuite) TestExcludedDuration() {
	s.Run("no time slots at all", func() {
		lp := &LoggingParams{}
		s.Require().Zero(lp.excludedDuration())
	})

	s.Run("only regular time slots", func() {
		lp := &LoggingParams{}
		lp.AddTimeSlotInt("slot1", 100)
		lp.AddTimeSlotDurationInMs("slot2", 2*time.Second)
		s.Require().Zero(lp.excludedDuration())
	})

	s.Run("excluded time slots are added to time_slots map too", func() {
		lp := &LoggingParams{}
		lp.AddTimeSlotInt("slot1", 100)
		lp.AddExcludedTimeSlotInt("slot2", 200)
		lp.AddExcludedTimeSlotDurationInMs("slot3", 300*time.Millisecond)

		s.Require().Equal(loggableIntMap{
			"slot1": 100 * time.Millisecond,
			"slot2": 200 * time.Millisecond,
			"slot3": 300 * time.Millisecond,
		}, lp.getTimeSlots())
		s.Require().Equal(500*time.Millisecond, lp.excludedDuration())
	})

	s.Run("sub-millisecond slots are not truncated away", func() {
		lp := &LoggingParams{}
		for i := 0; i < 2000; i++ {
			lp.AddExcludedTimeSlotDurationInMs("slot", 900*time.Microsecond)
		}
		s.Require().Equal(1800*time.Millisecond, lp.excludedDuration())
		s.Require().Equal(loggableIntMap{"slot": 1800 * time.Millisecond}, lp.getTimeSlots())
	})

	s.Run("same slot added as excluded and not", func() {
		lp := &LoggingParams{}
		lp.AddTimeSlotInt("slot1", 100)
		lp.AddExcludedTimeSlotInt("slot1", 200)

		s.Require().Equal(loggableIntMap{"slot1": 300 * time.Millisecond}, lp.getTimeSlots())
		s.Require().Equal(200*time.Millisecond, lp.excludedDuration())
	})

	s.Run("concurrent adding", func() {
		const goroutines = 32
		lp := &LoggingParams{}
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

		s.Require().Equal(loggableIntMap{
			"slot":          goroutines * time.Millisecond,
			"excluded_slot": goroutines * time.Millisecond,
		}, lp.getTimeSlots())
		s.Require().Equal(goroutines*time.Millisecond, lp.excludedDuration())
	})
}

func (s *LoggingParamsTestSuite) TestAddTimeSlotInt() {
	lp := &LoggingParams{}

	// Test adding time slots
	lp.AddTimeSlotInt("slot1", 100)
	lp.AddTimeSlotInt("slot2", 200)

	timeSlots := lp.getTimeSlots()
	s.Require().Len(timeSlots, 2)
	s.Require().Equal(100*time.Millisecond, timeSlots["slot1"])
	s.Require().Equal(200*time.Millisecond, timeSlots["slot2"])

	// Test adding to existing slot (should accumulate)
	lp.AddTimeSlotInt("slot1", 50)
	timeSlots = lp.getTimeSlots()
	s.Require().Equal(150*time.Millisecond, timeSlots["slot1"])
	s.Require().Equal(200*time.Millisecond, timeSlots["slot2"])
}

func (s *LoggingParamsTestSuite) TestAddTimeSlotDurationInMs() {
	lp := &LoggingParams{}

	// Test adding duration-based time slots
	lp.AddTimeSlotDurationInMs("slot1", 1*time.Second)
	lp.AddTimeSlotDurationInMs("slot2", 2*time.Second)

	timeSlots := lp.getTimeSlots()
	s.Require().Len(timeSlots, 2)
	s.Require().Equal(1*time.Second, timeSlots["slot1"])
	s.Require().Equal(2*time.Second, timeSlots["slot2"])

	// Test adding to existing slot (should accumulate)
	lp.AddTimeSlotDurationInMs("slot1", 500*time.Millisecond)
	timeSlots = lp.getTimeSlots()
	s.Require().Equal(1500*time.Millisecond, timeSlots["slot1"])
}

func (s *LoggingParamsTestSuite) TestConcurrentAccess() {
	lp := &LoggingParams{}

	// Test concurrent access to time slots
	done := make(chan struct{})

	// Start multiple goroutines that add time slots
	for i := 0; i < 10; i++ {
		go func(val int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				lp.AddTimeSlotInt("concurrent_slot", int64(val))
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Check that all values were accumulated correctly
	timeSlots := lp.getTimeSlots()
	s.Require().Len(timeSlots, 1)

	// Expected value: sum of (0+1+2+...+9) * 100 = 45 * 100 = 4500ms
	expectedSum := time.Duration(0)
	for i := 0; i < 10; i++ {
		expectedSum += time.Duration(i) * 100 * time.Millisecond
	}
	s.Require().Equal(expectedSum, timeSlots["concurrent_slot"])
}

func (s *LoggingParamsTestSuite) TestEmptyTimeSlots() {
	lp := &LoggingParams{}

	// Test getting time slots when none have been added
	timeSlots := lp.getTimeSlots()
	s.Require().Nil(timeSlots)

	// Test adding a field and then getting time slots
	lp.ExtendFields(log.String("field1", "value1"))
	timeSlots = lp.getTimeSlots()
	s.Require().Nil(timeSlots)
}

func (s *LoggingParamsTestSuite) TestMixedUsage() {
	lp := &LoggingParams{}

	// Test mixing fields and time slots
	lp.ExtendFields(log.String("field1", "value1"))
	lp.AddTimeSlotInt("slot1", 100)
	lp.ExtendFields(log.Int("field2", 42))
	lp.AddTimeSlotDurationInMs("slot2", 1*time.Second)

	// Check fields
	s.Require().Len(lp.fields, 2)
	s.Require().Equal("field1", lp.fields[0].Key)
	s.Require().Equal("field2", lp.fields[1].Key)

	// Check time slots
	timeSlots := lp.getTimeSlots()
	s.Require().Len(timeSlots, 2)
	s.Require().Equal(100*time.Millisecond, timeSlots["slot1"])
	s.Require().Equal(1*time.Second, timeSlots["slot2"])
}

func (s *LoggingParamsTestSuite) TestLoggableIntMapEncodeLogfObject() {
	lim := loggableIntMap{
		"slot1": 100 * time.Millisecond,
		"slot2": 200 * time.Millisecond,
		"slot3": 1500 * time.Microsecond,
	}

	// Test that the map is not nil and has expected values
	s.Require().Len(lim, 3)
	s.Require().Equal(100*time.Millisecond, lim["slot1"])
	s.Require().Equal(200*time.Millisecond, lim["slot2"])

	// Durations are kept at full precision, but encoded as integer milliseconds.
	var buf bytes.Buffer
	logtest.NewLoggerWithOpts(logtest.LoggerOpts{Output: &buf}).
		Info("test", log.Field{Key: "time_slots", Type: logf.FieldTypeObject, Any: lim})
	s.Require().Contains(buf.String(), `"slot1":100`)
	s.Require().Contains(buf.String(), `"slot2":200`)
	s.Require().Contains(buf.String(), `"slot3":1`)
}

func (s *LoggingParamsTestSuite) TestLoggableIntMapEncodeLogfObjectEmpty() {
	lim := loggableIntMap{}

	// Test that empty map works as expected
	s.Require().Empty(lim)
}

func (s *LoggingParamsTestSuite) TestIntegration() {
	lp := &LoggingParams{}

	// Simulate a real usage scenario
	lp.ExtendFields(log.String("service", "test-service"))
	lp.AddTimeSlotDurationInMs("db_query", 50*time.Millisecond)
	lp.AddTimeSlotDurationInMs("external_api", 100*time.Millisecond)
	lp.ExtendFields(log.Int("retry_count", 2))
	lp.AddTimeSlotDurationInMs("db_query", 25*time.Millisecond) // Additional query

	// Verify final state
	s.Require().Len(lp.fields, 2)
	s.Require().Equal("service", lp.fields[0].Key)
	s.Require().Equal("test-service", string(lp.fields[0].Bytes))
	s.Require().Equal("retry_count", lp.fields[1].Key)
	s.Require().Equal(int64(2), lp.fields[1].Int)

	timeSlots := lp.getTimeSlots()
	s.Require().Len(timeSlots, 2)
	s.Require().Equal(75*time.Millisecond, timeSlots["db_query"]) // 50 + 25 = 75ms
	s.Require().Equal(100*time.Millisecond, timeSlots["external_api"])

	// Test creating the time_slots field for logging
	lp.fields = append(lp.fields, log.Field{
		Key:  "time_slots",
		Type: logf.FieldTypeObject,
		Any:  lp.getTimeSlots(),
	})

	s.Require().Len(lp.fields, 3)
	s.Require().Equal("time_slots", lp.fields[2].Key)
	s.Require().Equal(logf.FieldTypeObject, lp.fields[2].Type)
	s.Require().Equal(lp.getTimeSlots(), lp.fields[2].Any)
}
