package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
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

type OvercommittableResources struct {
	Cpu    *resource.Quantity `json:"cpu,omitempty"`
	Memory *resource.Quantity `json:"memory,omitempty"`
}

type CodespecResources struct {
	Requests *OvercommittableResources `json:"requests,omitempty"`
	Limits   *OvercommittableResources `json:"limits,omitempty"`
	Gpu      *resource.Quantity        `json:"gpu,omitempty"`
}

type Codespec struct {
	Kind string `json:"kind"`
	CodespecMetadata
	Buffers     *BufferParameters  `json:"buffers,omitempty"`
	Image       string             `json:"image"`
	Command     []string           `json:"command,omitempty"`
	Args        []string           `json:"args,omitempty"`
	WorkingDir  string             `json:"workingDir,omitempty"`
	Env         map[string]string  `json:"env,omitempty"`
	Resources   *CodespecResources `json:"resources,omitempty"`
	MaxReplicas *int               `json:"maxReplicas,omitempty"`
	Endpoints   map[string]int     `json:"endpoints,omitempty"`
}

type CodespecMetadata struct {
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
}

type Page[T any] struct {
	Items    []T    `json:"items"`
	NextLink string `json:"nextLink,omitempty"`
}

type RunCodeTarget struct {
	Codespec CodespecRef       `json:"codespec"`
	Buffers  map[string]string `json:"buffers,omitempty"`
	NodePool string            `json:"nodePool,omitempty"`
	Replicas int               `json:"replicas,omitempty"`
}

type CodespecRef struct {
	Named  *NamedCodespecRef  `json:"-"`
	Inline *InlineCodespecRef `json:"-"`
}

type NamedCodespecRef string

type InlineCodespecRef Codespec

func (ref *CodespecRef) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&ref.Named); err == nil {
		return nil
	}
	ref.Named = nil
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&ref.Inline); err != nil {
		return fmt.Errorf("invalid codespec ref: %w", err)
	}

	return nil
}

func (ref CodespecRef) MarshalJSON() ([]byte, error) {
	if ref.Named != nil {
		return json.Marshal(ref.Named)
	}
	if ref.Inline != nil {
		return json.Marshal(ref.Inline)
	}
	return nil, fmt.Errorf("invalid codespec ref")
}

type Run struct {
	RunMetadata
	Job            RunCodeTarget     `json:"job,omitempty"`
	Worker         *RunCodeTarget    `json:"worker,omitempty"`
	Buffers        map[string]string `json:"buffers,omitempty"`
	Cluster        string            `json:"cluster,omitempty"`
	TimeoutSeconds *int              `json:"timeoutSeconds,omitempty"`
}

type RunMetadata struct {
	Id           int64      `json:"id,omitempty"`
	Status       string     `json:"status,omitempty"`
	StatusReason string     `json:"statusReason,omitempty"`
	RunningCount *int       `json:"runningCount,omitempty"`
	CreatedAt    time.Time  `json:"createdAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

type ErrorResponse struct {
	Error ErrorInfo `json:"error"`
}

type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type NodePool struct {
	Name   string `json:"name"`
	VmSize string `json:"vmSize"`
}

type Cluster struct {
	Name      string     `json:"name"`
	NodePools []NodePool `json:"nodePools"`
}
