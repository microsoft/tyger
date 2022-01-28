package model

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNotFound = errors.New("the resource was not found")
)

type ServiceMetadata struct {
	Authority string `json:"authority"`
	Audience  string `json:"audience"`
}

type Buffer struct {
	Id string `json:"id"`
}

type BufferAccess struct {
	Uri string `json:"uri"`
}

type BufferParameters struct {
	Inputs  []string `json:"inputs,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
}

type Codespec struct {
	Buffers    *BufferParameters `json:"buffers,omitempty"`
	Image      string            `json:"image"`
	Command    []string          `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	WorkingDir string            `json:"workingDir,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

func (c Codespec) Validate() error {
	if c.Image == "" {
		return &ValidationError{Message: "The image property cannot be empty"}
	}

	if c.Buffers != nil {
		lowerNames := make(map[string]string)
		combined := make([]string, 0)
		combined = append(combined, c.Buffers.Inputs...)
		combined = append(combined, c.Buffers.Outputs...)
		for _, v := range combined {
			if v == "" {
				return &ValidationError{Message: "A buffer name cannot be empty"}
			}

			if strings.Contains(v, "/") {
				return &ValidationError{Message: fmt.Sprintf("The buffer named '%s' cannot contain '/'", v)}
			}

			lowerV := strings.ToLower(v)
			if _, found := lowerNames[lowerV]; found {
				return &ValidationError{Message: fmt.Sprintf("All buffer names must be unique across inputs and outputs. Buffer names are case-insensitive. '%s' is duplicated", v)}
			}

			lowerNames[strings.ToLower(v)] = v
		}
	}

	return nil
}

type Run struct {
	Id       string            `json:"id,omitempty"`
	Buffers  map[string]string `json:"buffers,omitempty"`
	Codespec string            `json:"codespec"`
	Status   string            `json:"status,omitempty"`
}

type ValidationError struct {
	Message string
}

func (mr *ValidationError) Error() string {
	return mr.Message
}

type ErrorResponse struct {
	Error ErrorInfo `json:"error"`
}

type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
