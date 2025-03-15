package langs

import (
	"encoding/json"
	"fmt"
	"io"
)

type Error struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (e Error) Wrap(err error) Error {
	return Error{Code: e.Code, Msg: fmt.Sprintf("%s: %s", e.Msg, err)}
}

func (e Error) Error() string {
	return fmt.Sprintf("ENO%d: %s", e.Code, e.Msg)
}

func (e Error) MarshalTo(w io.Writer) {
	json.NewEncoder(w).Encode(e)
}

func Err(err error) Error {
	if knownErr, ok := err.(Error); ok {
		return knownErr
	}
	return Error{Code: 5000, Msg: err.Error()}
}

type Data[T any] struct {
	Error
	Data T `json:"data"`
}

func (d Data[T]) MarshalTo(w io.Writer) {
	json.NewEncoder(w).Encode(d)
}
