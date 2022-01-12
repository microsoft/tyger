package model

import "errors"

var (
	ErrNotFound = errors.New("the resource was not found")
)

type BufferParameter struct {
	Name      string `json:"name"`
	Writeable bool   `json:"writeable"`
}

type Codespec struct {
	BufferParameters []BufferParameter `json:"bufferParameters,omitempty"`
	Image            string            `json:"image"`
	Command          []string          `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	WorkingDir       string            `json:"workingDir,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
}

type Run struct {
	Id       string            `json:"id,omitempty"`
	Buffers  map[string]string `json:"buffers,omitempty"`
	CodeSpec string            `json:"codeSpec"`
	Status   string            `json:"status,omitempty"`
}

type ValidationError struct {
	Message string
}

func (mr *ValidationError) Error() string {
	return mr.Message
}
