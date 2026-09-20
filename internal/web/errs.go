package web

import "fmt"

type httpError struct{ msg string }

func (e *httpError) Error() string { return e.msg }

func errBad(msg string) error       { return &httpError{msg: msg} }
func errNotFound(kind string) error { return &httpError{msg: fmt.Sprintf("%s not found", kind)} }
