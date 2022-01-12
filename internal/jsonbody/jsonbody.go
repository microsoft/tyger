// Heavily inspired by https://www.alexedwards.net/blog/how-to-properly-parse-a-json-request-body

package jsonbody

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type MalformedBodyError struct {
	StatusCode int
	Message    string
}

func (mr *MalformedBodyError) Error() string {
	return mr.Message
}

func DecodeJSONBody(w http.ResponseWriter, r *http.Request, dst interface{}) error {
	contentType := r.Header.Get("Content-Type")
	if contentType != "" && contentType != "application/json" {
		msg := "Content-Type header is not application/json"
		return &MalformedBodyError{StatusCode: http.StatusUnsupportedMediaType, Message: msg}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1*1024*1024)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(&dst)
	if err != nil {
		var syntaxError *json.SyntaxError
		var unmarshalTypeError *json.UnmarshalTypeError

		switch {
		case errors.As(err, &syntaxError):
			msg := fmt.Sprintf("Request body contains badly-formed JSON (at position %d)", syntaxError.Offset)
			return &MalformedBodyError{StatusCode: http.StatusBadRequest, Message: msg}

		case errors.Is(err, io.ErrUnexpectedEOF):
			msg := "Request body contains badly-formed JSON"
			return &MalformedBodyError{StatusCode: http.StatusBadRequest, Message: msg}

		case errors.As(err, &unmarshalTypeError):
			msg := fmt.Sprintf("Request body contains an invalid value for the %q field (at position %d)", unmarshalTypeError.Field, unmarshalTypeError.Offset)
			return &MalformedBodyError{StatusCode: http.StatusBadRequest, Message: msg}

		case strings.HasPrefix(err.Error(), "json: unknown field "):
			fieldName := strings.TrimPrefix(err.Error(), "json: unknown field ")
			msg := fmt.Sprintf("Request body contains unknown field %s", fieldName)
			return &MalformedBodyError{StatusCode: http.StatusBadRequest, Message: msg}

		case errors.Is(err, io.EOF):
			msg := "Request body must not be empty"
			return &MalformedBodyError{StatusCode: http.StatusBadRequest, Message: msg}

		case err.Error() == "http: request body too large":
			msg := "Request body must not be larger than 1MB"
			return &MalformedBodyError{StatusCode: http.StatusRequestEntityTooLarge, Message: msg}

		default:
			return err
		}
	}

	if dec.More() {
		msg := "Request body must only contain a single JSON object"
		return &MalformedBodyError{StatusCode: http.StatusBadRequest, Message: msg}
	}

	return nil
}
