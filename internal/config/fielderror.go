package config

import "errors"

// FieldError ties a profile refusal to the profile field it is about. Path is
// the field as the profile editor addresses it: a JSON key, with an index where
// the refusal is about one entry ("context_paths[1]", "github.owners[0].app",
// "options.model"). Error is the wrapped sentence, unchanged, so a client that
// matches on the text keeps working; Unwrap keeps sentinels and typed errors
// (ErrContextNeedsBox, *UndefinedRefError) reachable through errors.Is and
// errors.As.
type FieldError struct {
	Path string
	Err  error
}

func (e *FieldError) Error() string { return e.Err.Error() }

func (e *FieldError) Unwrap() error { return e.Err }

// WithField reports err as a refusal about the field at path. A nil err stays
// nil. An err that already carries a path, on its own or behind a %w wrapper,
// is returned as it is: the site that knows the index runs first, so the
// innermost (most specific) path wins over the bare one a caller adds around it.
func WithField(path string, err error) error {
	if err == nil {
		return nil
	}
	var fe *FieldError
	if errors.As(err, &fe) {
		return err
	}
	return &FieldError{Path: path, Err: err}
}
