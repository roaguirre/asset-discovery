package runservice

import (
	"errors"
	"net/http"
)

var (
	ErrForbidden = errors.New("forbidden")
	ErrNotFound  = errors.New("not found")
)

type classifiedError struct {
	class   error
	message string
}

func (e *classifiedError) Error() string {
	return e.message
}

func (e *classifiedError) Unwrap() error {
	return e.class
}

func newForbiddenError(message string) error {
	return &classifiedError{class: ErrForbidden, message: message}
}

func newNotFoundError(message string) error {
	return &classifiedError{class: ErrNotFound, message: message}
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}
