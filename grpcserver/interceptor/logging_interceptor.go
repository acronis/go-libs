/*
Copyright © 2025 Acronis International GmbH.

Released under MIT license.
*/

package interceptor

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/acronis/go-appkit/log"
)

const headerUserAgentKey = "user-agent"

const (
	defaultSlowCallThreshold = 1 * time.Second
)

// UnaryCustomLoggerProvider returns a custom logger or nil based on the gRPC context and method info.
type UnaryCustomLoggerProvider func(ctx context.Context, info *grpc.UnaryServerInfo) log.FieldLogger

// StreamCustomLoggerProvider returns a custom logger or nil based on the gRPC context and stream method info.
type StreamCustomLoggerProvider func(ctx context.Context, info *grpc.StreamServerInfo) log.FieldLogger

// LoggingOption represents a configuration option for the logging interceptor.
type LoggingOption func(*loggingOptions)

type loggingOptions struct {
	callStart                  bool
	callHeaders                map[string]string
	excludedMethods            []string
	addCallInfoToLogger        bool
	slowCallThreshold          time.Duration
	timeSlotsThreshold         time.Duration
	unaryCustomLoggerProvider  UnaryCustomLoggerProvider
	streamCustomLoggerProvider StreamCustomLoggerProvider
}

// WithLoggingCallStart enables logging of call start events.
func WithLoggingCallStart(logCallStart bool) LoggingOption {
	return func(opts *loggingOptions) {
		opts.callStart = logCallStart
	}
}

// WithLoggingCallHeaders specifies custom headers to log from gRPC metadata.
func WithLoggingCallHeaders(headers map[string]string) LoggingOption {
	return func(opts *loggingOptions) {
		opts.callHeaders = headers
	}
}

// WithLoggingExcludedMethods specifies gRPC methods to exclude from logging.
func WithLoggingExcludedMethods(methods ...string) LoggingOption {
	return func(opts *loggingOptions) {
		opts.excludedMethods = methods
	}
}

// WithLoggingAddCallInfoToLogger adds call information to the logger context.
func WithLoggingAddCallInfoToLogger(addCallInfo bool) LoggingOption {
	return func(opts *loggingOptions) {
		opts.addCallInfoToLogger = addCallInfo
	}
}

// WithLoggingSlowCallThreshold sets the threshold for slow call detection.
func WithLoggingSlowCallThreshold(threshold time.Duration) LoggingOption {
	return func(opts *loggingOptions) {
		opts.slowCallThreshold = threshold
	}
}

// WithLoggingTimeSlotsThreshold sets the threshold for adding time_slots structure for calls analysis.
func WithLoggingTimeSlotsThreshold(threshold time.Duration) LoggingOption {
	return func(opts *loggingOptions) {
		opts.timeSlotsThreshold = threshold
	}
}

// WithLoggingUnaryCustomLoggerProvider sets a custom logger provider function.
func WithLoggingUnaryCustomLoggerProvider(provider UnaryCustomLoggerProvider) LoggingOption {
	return func(opts *loggingOptions) {
		opts.unaryCustomLoggerProvider = provider
	}
}

// WithLoggingStreamCustomLoggerProvider sets a custom logger provider function for stream interceptors.
func WithLoggingStreamCustomLoggerProvider(provider StreamCustomLoggerProvider) LoggingOption {
	return func(opts *loggingOptions) {
		opts.streamCustomLoggerProvider = provider
	}
}

// LoggingUnaryInterceptor is a gRPC unary interceptor that logs the start and end of each RPC call.
func LoggingUnaryInterceptor(logger log.FieldLogger, options ...LoggingOption) func(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	opts := &loggingOptions{slowCallThreshold: defaultSlowCallThreshold}
	for _, option := range options {
		option(opts)
	}
	if opts.timeSlotsThreshold == 0 {
		opts.timeSlotsThreshold = opts.slowCallThreshold
	}
	return func(
		ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (interface{}, error) {
		loggerProvider := func(ctx context.Context) log.FieldLogger {
			if opts.unaryCustomLoggerProvider != nil {
				if l := opts.unaryCustomLoggerProvider(ctx, info); l != nil {
					return l
				}
			}
			return logger
		}
		var resp any
		var err error
		loggingServerInterceptor(ctx, CallMethodTypeUnary, loggerProvider, info.FullMethod, opts,
			func(ctx context.Context) error {
				resp, err = handler(ctx, req)
				return err
			})
		return resp, err
	}
}

// LoggingStreamInterceptor is a gRPC stream interceptor that logs the start and end of each RPC call.
func LoggingStreamInterceptor(logger log.FieldLogger, options ...LoggingOption) func(
	srv interface{},
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	opts := &loggingOptions{slowCallThreshold: defaultSlowCallThreshold}
	for _, option := range options {
		option(opts)
	}
	if opts.timeSlotsThreshold == 0 {
		opts.timeSlotsThreshold = opts.slowCallThreshold
	}
	return func(
		srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler,
	) error {
		loggerProvider := func(ctx context.Context) log.FieldLogger {
			if opts.streamCustomLoggerProvider != nil {
				if l := opts.streamCustomLoggerProvider(ctx, info); l != nil {
					return l
				}
			}
			return logger
		}
		var err error
		loggingServerInterceptor(ss.Context(), CallMethodTypeStream, loggerProvider, info.FullMethod, opts,
			func(ctx context.Context) error {
				wrappedStream := &WrappedServerStream{ServerStream: ss, Ctx: ctx}
				err = handler(srv, wrappedStream)
				return err
			})
		return err
	}
}

func loggingServerInterceptor(
	ctx context.Context,
	methodType CallMethodType,
	loggerProvider func(ctx context.Context) log.FieldLogger,
	fullMethod string,
	opts *loggingOptions,
	handler func(ctx context.Context) error,
) {
	startTime := GetCallStartTimeFromContext(ctx)
	if startTime.IsZero() {
		startTime = time.Now()
		ctx = NewContextWithCallStartTime(ctx, startTime)
	}

	loggerForNext := loggerProvider(ctx)
	loggerForNext = loggerForNext.With(
		log.String("request_id", GetRequestIDFromContext(ctx)),
		log.String("int_request_id", GetInternalRequestIDFromContext(ctx)),
		log.String("trace_id", GetTraceIDFromContext(ctx)),
	)

	logFields := buildCallInfoLogFields(ctx, fullMethod, methodType, opts)
	logger := loggerForNext.With(logFields...)
	if opts.addCallInfoToLogger {
		loggerForNext = logger
	}

	noLog := isLoggingDisabled(fullMethod, opts.excludedMethods)

	if opts.callStart && !noLog {
		logger.Info("gRPC call started")
	}

	lp := &LoggingParams{}
	ctx = NewContextWithLoggingParams(NewContextWithLogger(ctx, loggerForNext), lp)

	err := handler(ctx)
	// totalDuration - for logging of total call time
	// effectiveDuration - for calculating the thresholds
	totalDuration, excludedDuration, effectiveDuration := calculateDurations(startTime, lp)

	grpcCode := status.Code(err)
	if !noLog || grpcCode != codes.OK { // Log if not excluded or if there's an error
		if effectiveDuration >= opts.timeSlotsThreshold {
			lp.fields = append(lp.fields, log.Object("time_slots", lp.getTimeSlots()))
		}
		if effectiveDuration >= opts.slowCallThreshold {
			lp.fields = append(lp.fields, log.Bool("slow_request", true))
		}
		logFields = append(
			logFields,
			log.String("grpc_code", grpcCode.String()),
			log.Int64("duration_ms", totalDuration.Milliseconds()),
		)
		if excludedDuration > 0 {
			logFields = append(
				logFields,
				log.Int64("excluded_duration_ms", excludedDuration.Milliseconds()),
				log.Int64("effective_duration_ms", effectiveDuration.Milliseconds()),
			)
		}
		if err != nil {
			logFields = append(logFields, log.String("grpc_error", err.Error()))
		}
		logger.Info(
			fmt.Sprintf("gRPC call finished in %.3fs", totalDuration.Seconds()),
			append(logFields, lp.fields...)...,
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

// buildCallInfoLogFields builds the common log fields for both unary and stream interceptors
func buildCallInfoLogFields(
	ctx context.Context, fullMethod string, methodType CallMethodType, opts *loggingOptions,
) []log.Field {
	service, method := splitFullMethodName(fullMethod)
	var remoteAddr string
	var remoteAddrIP string
	var remoteAddrPort uint16
	if p, ok := peer.FromContext(ctx); ok {
		remoteAddr = p.Addr.String()
		if addrIP, addrPort, err := net.SplitHostPort(remoteAddr); err == nil {
			remoteAddrIP = addrIP
			if port, pErr := strconv.ParseUint(addrPort, 10, 16); pErr == nil {
				remoteAddrPort = uint16(port)
			}
		}
	}

	var userAgent string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if userAgentList := md.Get(headerUserAgentKey); len(userAgentList) > 0 {
			userAgent = userAgentList[0]
		}
	}

	logFields := make([]log.Field, 0, 8)
	logFields = append(
		logFields,
		log.String("grpc_service", service),
		log.String("grpc_method", method),
		log.String("grpc_method_type", string(methodType)),
		log.String("remote_addr", remoteAddr),
		log.String("user_agent", userAgent),
	)

	if remoteAddrIP != "" {
		logFields = append(logFields, log.String("remote_addr_ip", remoteAddrIP))
		if remoteAddrPort != 0 {
			logFields = append(logFields, log.Uint16("remote_addr_port", remoteAddrPort))
		}
	}

	if len(opts.callHeaders) > 0 {
		// Add custom headers from metadata
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			for headerName, logKey := range opts.callHeaders {
				if headerValues := md.Get(headerName); len(headerValues) > 0 {
					logFields = append(logFields, log.String(logKey, headerValues[0]))
				}
			}
		}
	}

	return logFields
}

func splitFullMethodName(fullMethod string) (service string, method string) {
	const unknown = "unknown"
	fullMethod = strings.TrimPrefix(fullMethod, "/") // remove leading slash
	if i := strings.Index(fullMethod, "/"); i >= 0 {
		return fullMethod[:i], fullMethod[i+1:]
	}
	return unknown, unknown
}

func isLoggingDisabled(fullMethod string, excludedMethods []string) bool {
	for _, method := range excludedMethods {
		if fullMethod == method {
			return true
		}
	}
	return false
}
