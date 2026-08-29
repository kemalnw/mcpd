package filesystem

import "errors"

var (
	ErrUnsupportedFormat = errors.New("unsupported file format")
	ErrNotText           = errors.New("file is not text")
	ErrInvalidEdit       = errors.New("invalid edit request")
)
