// Package service implements application business rules and transaction orchestration.
package service

import "errors"

var (
	ErrInvalidEmail           = errors.New("invalid email")
	ErrPasswordTooShort       = errors.New("password too short")
	ErrDisplayNameTooLong     = errors.New("display name too long")
	ErrEmailAlreadyRegistered = errors.New("email already registered")
	ErrInvalidCredentials     = errors.New("invalid credentials")
	ErrUnauthenticated        = errors.New("unauthenticated")
	ErrConversationNotFound   = errors.New("conversation not found")
	ErrEmptyConversationTitle = errors.New("empty conversation title")
	ErrInvalidMessageLength   = errors.New("invalid message length")
	ErrInvalidTemperature     = errors.New("invalid temperature")
	ErrOutputFormatNotFound   = errors.New("output format not found")
	ErrMessageNotFound        = errors.New("message not found")
	ErrMessageNotRegenerable  = errors.New("message cannot be regenerated")
	ErrMessageNotEditable     = errors.New("message cannot be edited")
)

// ProviderSelectionError indicates a client selected a provider or model that
// is unavailable in the current workspace.
type ProviderSelectionError struct{ err error }

func (e *ProviderSelectionError) Error() string { return e.err.Error() }

func (e *ProviderSelectionError) Unwrap() error { return e.err }
