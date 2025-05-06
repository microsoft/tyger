// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

type ServiceMetadata struct {
	Authority      string   `json:"authority,omitempty"`
	Audience       string   `json:"audience,omitempty"`
	ApiAppUri      string   `json:"serverAppUri,omitempty"`
	ApiAppId       string   `json:"serverAppId,omitempty"`
	CliAppUri      string   `json:"cliAppUri,omitempty"`
	CliAppId       string   `json:"cliAppId,omitempty"`
	DataPlaneProxy string   `json:"dataPlaneProxy,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	ApiVersions    []string `json:"apiVersions,omitempty"`
}

type Buffer struct {
	Id        string            `json:"id"`
	CreatedAt time.Time         `json:"createdAt"`
	Location  string            `json:"location,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	ETag      string            `json:"eTag,omitempty"`

	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type BufferAccess struct {
	Uri string `json:"uri"`
}

type BufferParameters struct {
	Inputs  []string `json:"inputs,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
}

type StorageAccount struct {
	Name     string `json:"name"`
	Location string `json:"location"`
	Endpoint string `json:"endpoint"`
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

type Socket struct {
	Port         int    `json:"port"`
	InputBuffer  string `json:"inputBuffer,omitempty"`
	OutputBuffer string `json:"outputBuffer,omitempty"`
}

type Codespec struct {
	Kind             string `json:"kind"`
	CodespecMetadata `json:",inline"`
	Buffers          *BufferParameters  `json:"buffers,omitempty"`
	Sockets          []Socket           `json:"sockets,omitempty"`
	Image            string             `json:"image"`
	Command          []string           `json:"command,omitempty"`
	Args             []string           `json:"args,omitempty"`
	WorkingDir       string             `json:"workingDir,omitempty"`
	Env              map[string]string  `json:"env,omitempty"`
	Identity         string             `json:"identity,omitempty"`
	Resources        *CodespecResources `json:"resources,omitempty"`
	MaxReplicas      *int               `json:"maxReplicas,omitempty"`
	Endpoints        map[string]int     `json:"endpoints,omitempty"`
}

type CodespecMetadata struct {
	Name      *string    `json:"name,omitempty"`
	Version   *int       `json:"version,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
}

type Page[T any] struct {
	Items    []T    `json:"items"`
	NextLink string `json:"nextLink,omitempty"`
}

type RunCodeTarget struct {
	Codespec  CodespecRef       `json:"codespec"`
	Buffers   map[string]string `json:"buffers,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	BufferTtl string            `json:"bufferTtl,omitempty"`
	NodePool  string            `json:"nodePool,omitempty"`
	Replicas  int               `json:"replicas,omitempty"`
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
	Kind            string            `json:"kind,omitempty"`
	Job             RunCodeTarget     `json:"job,omitempty"`
	Worker          *RunCodeTarget    `json:"worker,omitempty"`
	Cluster         string            `json:"cluster,omitempty"`
	TimeoutSeconds  *int              `json:"timeoutSeconds,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	ETag            string            `json:"eTag,omitempty"`
	BufferAccessTtl string            `json:"bufferAccessTtl,omitempty"`
}

type RunStatus uint

const (
	// The run has been created, but is waiting to start
	Pending RunStatus = iota

	// The Run is currently running
	Running

	// Indicates that the run has failed, see the StatusReason field for information on why.
	Failed

	// Indicates that the run has compeleted successfully
	Succeeded

	// The run is in the process of being canceled.
	Canceling

	// The run was canceled.
	Canceled
)

var RunStatuses = []RunStatus{Pending, Running, Failed, Succeeded, Canceling, Canceled}

var stringToRunStatus = map[string]RunStatus{
	"Pending":   Pending,
	"Running":   Running,
	"Failed":    Failed,
	"Succeeded": Succeeded,
	"Canceling": Canceling,
	"Canceled":  Canceled,
}

var runStatusToString = func() map[RunStatus]string {
	m := make(map[RunStatus]string)
	for k, v := range stringToRunStatus {
		m[v] = k
	}
	return m
}()

func (status RunStatus) String() string {
	return runStatusToString[status]
}

func (status RunStatus) MarshalJSON() ([]byte, error) {
	buffer := bytes.NewBufferString("\"")
	buffer.WriteString(runStatusToString[status])
	buffer.WriteString("\"")
	return buffer.Bytes(), nil
}

func (status *RunStatus) UnmarshalJSON(b []byte) error {
	var statusString string
	error := json.Unmarshal(b, &statusString)
	if error != nil {
		return error
	}

	value, success := stringToRunStatus[statusString]
	if success {
		*status = value
	} else {
		return fmt.Errorf("invalid status value: %v", statusString)
	}

	return nil
}

type RunMetadata struct {
	Id           int64             `json:"id,omitempty"`
	Status       *RunStatus        `json:"status,omitempty"`
	StatusReason string            `json:"statusReason,omitempty"`
	RunningCount *int              `json:"runningCount,omitempty"`
	CreatedAt    time.Time         `json:"createdAt,omitempty"`
	StartedAt    *time.Time        `json:"startedAt,omitempty"`
	FinishedAt   *time.Time        `json:"finishedAt,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

type ErrorResponse struct {
	Error ErrorInfo `json:"error"`
}

type ErrorInfo struct {
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	ApiVersions []string `json:"apiVersions,omitempty"`
}

func (e *ErrorInfo) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type NodePool struct {
	Name   string `json:"name"`
	VmSize string `json:"vmSize"`
}

type Cluster struct {
	Name      string     `json:"name"`
	Location  string     `json:"location"`
	NodePools []NodePool `json:"nodePools"`
}

type ExportBuffersRequest struct {
	SourceStorageAccountName   string            `json:"sourceStorageAccountName"`
	DestinationStorageEndpoint string            `json:"destinationStorageEndpoint"`
	Filters                    map[string]string `json:"filters,omitempty"`
	HashIds                    bool              `json:"hashIds,omitempty"`
}

type ImportBuffersRequest struct {
	StorageAccountName string `json:"storageAccountName"`
}
