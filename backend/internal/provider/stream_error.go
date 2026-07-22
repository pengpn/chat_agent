package provider

import (
	"context"
	"errors"
	"io"
	"net"
)

type StreamErrorKind string

const (
	StreamErrorCanceled       StreamErrorKind = "canceled"
	StreamErrorConnectTimeout StreamErrorKind = "connect_timeout"
	StreamErrorTTFTTimeout    StreamErrorKind = "ttft_timeout"
	StreamErrorTotalTimeout   StreamErrorKind = "total_timeout"
	StreamErrorDisconnected   StreamErrorKind = "disconnected"
	StreamErrorUpstream       StreamErrorKind = "upstream"
)

// StreamError preserves a stable error category across Provider adapters.
type StreamError struct {
	Kind StreamErrorKind
	Err  error
}

func (e *StreamError) Error() string { return e.Err.Error() }
func (e *StreamError) Unwrap() error { return e.Err }

func NewStreamError(kind StreamErrorKind, err error) error {
	if err == nil {
		err = errors.New(string(kind))
	}
	return &StreamError{Kind: kind, Err: err}
}

func ClassifyStreamError(ctx context.Context, err error) error {
	var streamErr *StreamError
	if errors.As(err, &streamErr) {
		return err
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return NewStreamError(StreamErrorCanceled, context.Canceled)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return NewStreamError(StreamErrorTotalTimeout, context.DeadlineExceeded)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return NewStreamError(StreamErrorDisconnected, err)
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		if networkErr.Timeout() {
			return NewStreamError(StreamErrorConnectTimeout, err)
		}
		return NewStreamError(StreamErrorDisconnected, err)
	}
	return NewStreamError(StreamErrorUpstream, err)
}

func ClassifyConnectError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return NewStreamError(StreamErrorCanceled, context.Canceled)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return NewStreamError(StreamErrorTotalTimeout, context.DeadlineExceeded)
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return NewStreamError(StreamErrorConnectTimeout, err)
	}
	return ClassifyStreamError(ctx, err)
}

func StreamErrorKindOf(err error) StreamErrorKind {
	var streamErr *StreamError
	if errors.As(err, &streamErr) {
		return streamErr.Kind
	}
	return StreamErrorUpstream
}
