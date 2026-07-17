// Package domain contains business concepts shared across application layers.
package domain

import "errors"

var (
	ErrNotFound      = errors.New("domain record not found")
	ErrAlreadyExists = errors.New("domain record already exists")
)
