package process

import "errors"

var (
	ErrSessionNotFound = errors.New("process session not found")
	ErrProcessExited   = errors.New("process has already exited")
	ErrInvalidPTYMode  = errors.New("invalid PTY mode")
)
