package store

import "errors"

var ErrAlreadyExists = errors.New("app already exists")
var ErrNotFound = errors.New("app not found")
