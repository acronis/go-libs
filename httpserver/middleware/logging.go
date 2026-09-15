/*
Copyright © 2024 Acronis International GmbH.

Released under MIT license.
*/

package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ssgreg/logf"

	"github.com/acronis/go-appkit/log"
)

const (
	// LoggingSecretQueryPlaceholder represents a placeholder that will be used for secret query parameters.
	LoggingSecretQueryPlaceholder = "_HIDDEN_"

	userAgentLogFieldKey = "user_agent"

	headerForwardedFor = "X-Forwarded-For"
	headerRealIP       = "X-Real-IP"
)

// CustomLoggerProvider returns a custom logger or nil based on the request.
type CustomLoggerProvider func(r *http.Request) log.FieldLogger

// LoggingOpts represents an options for Logging middleware.
type LoggingOpts struct {
	RequestStart           bool
	RequestHeaders         map[string]string
	ExcludedEndpoints      []string
	SecretQueryParams      []string
	AddRequestInfoToLogger bool
	SlowRequestThreshold   time.Duration // controls when to include "slow_request" flag into final log message
	TimeSlotsThreshold     time.Duration // controls when to include "time_slots" field group into final log message
	// If CustomLoggerProvider is not set or returns nil, loggingHandler.logger will be used.
	CustomLoggerProvider CustomLoggerProvider
}

type loggingHandler struct {
	next   http.Handler
	logger log.FieldLogger
	opts   LoggingOpts
}

// Logging is a middleware that logs info about HTTP request and response.
// Also, it puts logger (with external and internal request's ids in fields) into request's context.
func Logging(logger log.FieldLogger) func(next http.Handler) http.Handler {
	return LoggingWithOpts(logger, LoggingOpts{RequestStart: false})
}

// LoggingWithOpts is a more configurable version of Logging middleware.
func LoggingWithOpts(logger log.FieldLogger, opts LoggingOpts) func(next http.Handler) http.Handler {
	if opts.SlowRequestThreshold == 0 {
		opts.SlowRequestThreshold = 1 * time.Second
	}
	if opts.TimeSlotsThreshold == 0 {
		opts.TimeSlotsThreshold = opts.SlowRequestThreshold
	}
	return func(next http.Handler) http.Handler {
		return &loggingHandler{next: next, logger: logger, opts: opts}
	}
}

func (h *loggingHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	startTime := GetRequestStartTimeFromContext(ctx)
	if startTime.IsZero() {
		startTime = time.Now()
		ctx = NewContextWithRequestStartTime(ctx, startTime)
	}

	loggerForNext := h.logger
	if h.opts.CustomLoggerProvider != nil {
		if l := h.opts.CustomLoggerProvider(r); l != nil {
			loggerForNext = l
		}
	}
	loggerForNext = loggerForNext.With(
		log.String("request_id", GetRequestIDFromContext(ctx)),
		log.String("int_request_id", GetInternalRequestIDFromContext(ctx)),
		log.String("trace_id", GetTraceIDFromContext(ctx)),
	)

	logFields := make([]log.Field, 0, 8)
	logFields = append(
		logFields,
		log.String("method", r.Method),
		log.String("uri", h.makeURIToLog(r)),
		log.String("remote_addr", r.RemoteAddr),
		log.Int64("content_length", r.ContentLength),
		log.String(userAgentLogFieldKey, r.UserAgent()),
	)

	logFields = addRemoteOriginAddrFields(logFields, r)

	for reqHeaderName, logKey := range h.opts.RequestHeaders {
		logFields = append(logFields, log.String(logKey, r.Header.Get(reqHeaderName)))
	}

	logger := loggerForNext.With(logFields...)
	if h.opts.AddRequestInfoToLogger {
		loggerForNext = logger
	}

	noLog := isLoggingDisabled(r.URL.Path, h.opts.ExcludedEndpoints)

	if h.opts.RequestStart && !noLog {
		logger.Info("request started")
	}

	lp := &LoggingParams{}
	r = r.WithContext(NewContextWithLoggingParams(NewContextWithLogger(ctx, loggerForNext), lp))
	wrw := WrapResponseWriterIfNeeded(rw, r.ProtoMajor)
	h.next.ServeHTTP(wrw, r)

	if !noLog || wrw.Status() >= http.StatusBadRequest {
		// totalDuration - for logging of total request time
		// effectiveDuration - for calculating the thresholds
		totalDuration, excludedDuration, effectiveDuration := calculateDurations(startTime, lp)
		if effectiveDuration >= h.opts.TimeSlotsThreshold {
			lp.AddTimeSlotDurationInMs("writing_response_ms", wrw.ElapsedTime())
			lp.fields = append(
				lp.fields,
				log.Field{Key: "time_slots", Type: logf.FieldTypeObject, Any: lp.getTimeSlots()},
			)
		}
		if effectiveDuration >= h.opts.SlowRequestThreshold {
			lp.fields = append(lp.fields, log.Bool("slow_request", true))
		}
		durationFields := []log.Field{
			log.Int64("duration_ms", totalDuration.Milliseconds()),
			log.DurationIn(totalDuration, time.Microsecond), // For backward compatibility, will be removed in the future.
			log.Int("status", wrw.Status()),
			log.Int("bytes_sent", wrw.BytesWritten()),
		}
		if excludedDuration > 0 {
			durationFields = append(
				durationFields,
				log.Int64("excluded_duration_ms", excludedDuration.Milliseconds()),
				log.Int64("effective_duration_ms", effectiveDuration.Milliseconds()),
			)
		}
		logger.Info(
			fmt.Sprintf("response completed in %.3fs", totalDuration.Seconds()),
			append(durationFields, lp.fields...)...,
		)
	}
}

func calculateDurations(start time.Time, lp *LoggingParams) (total, excluded, effective time.Duration) {
	total = time.Since(start)
	excluded = lp.excludedDuration()
	if e := total - excluded; e > 0 {
		effective = e
	}
	return
}

func (h *loggingHandler) makeURIToLog(r *http.Request) string {
	if len(h.opts.SecretQueryParams) == 0 || r.URL.RawQuery == "" {
		return r.RequestURI
	}
	queryValues := r.URL.Query()
	for _, k := range h.opts.SecretQueryParams {
		vals := queryValues[k]
		for i := range vals {
			if vals[i] != "" {
				vals[i] = LoggingSecretQueryPlaceholder
			}
		}
	}
	return r.URL.Path + "?" + queryValues.Encode()
}

func isLoggingDisabled(urlPath string, noLogEndpoints []string) bool {
	for _, endpoint := range noLogEndpoints {
		if urlPath == endpoint {
			return true
		}
	}
	return false
}

func addRemoteOriginAddrFields(logFields []log.Field, r *http.Request) []log.Field {
	if addrIP, addrPort, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		logFields = append(logFields, log.String("remote_addr_ip", addrIP))
		if port, pErr := strconv.ParseUint(addrPort, 10, 16); pErr == nil {
			logFields = append(logFields, log.Uint16("remote_addr_port", uint16(port)))
		}
	}
	if originAddr := getOriginAddr(r); originAddr != "" {
		logFields = append(logFields, log.String("origin_addr", originAddr))
	}
	return logFields
}

func getOriginAddr(r *http.Request) string {
	if forwardFor := r.Header.Get(headerForwardedFor); forwardFor != "" {
		remoteAddr := forwardFor
		first := strings.IndexByte(forwardFor, ',')
		if first != -1 {
			remoteAddr = forwardFor[:first]
		}
		return strings.TrimSpace(remoteAddr)
	}

	if realIP := r.Header.Get(headerRealIP); realIP != "" {
		return strings.TrimSpace(realIP)
	}

	return ""
}
