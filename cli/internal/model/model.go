package model

import "time"

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

type CodespecResources struct {
	Cpu    *string `json:"cpu,omitempty"`
	Memory *string `json:"memory,omitempty"`
	Gpu    *string `json:"gpu,omitempty"`
}

type NewCodespec struct {
	Kind        string             `json:"kind"`
	Buffers     *BufferParameters  `json:"buffers,omitempty"`
	Image       string             `json:"image"`
	Command     []string           `json:"command,omitempty"`
	Args        []string           `json:"args,omitempty"`
	WorkingDir  string             `json:"workingDir,omitempty"`
	Env         map[string]string  `json:"env,omitempty"`
	Resources   *CodespecResources `json:"resources,omitempty"`
	MaxReplicas int                `json:"maxReplicas"`
}

type Codespec struct {
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	NewCodespec
}

type Page[T any] struct {
	Items    []T    `json:"items"`
	NextLink string `json:"nextLink,omitempty"`
}

type RunCodeTarget struct {
	Codespec string            `json:"codespec"`
	Buffers  map[string]string `json:"buffers,omitempty"`
	NodePool string            `json:"nodePool,omitempty"`
	Replicas int               `json:"replicas"`
}

type NewRun struct {
	Job            RunCodeTarget     `json:"job,omitempty"`
	Worker         *RunCodeTarget    `json:"worker,omitempty"`
	Buffers        map[string]string `json:"buffers,omitempty"`
	Cluster        string            `json:"cluster"`
	TimeoutSeconds *int              `json:"timeoutSeconds,omitempty"`
}

type Run struct {
	Id           int64      `json:"id,omitempty"`
	Status       string     `json:"status,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	RunningCount int        `json:"runningCount"`
	CreatedAt    time.Time  `json:"createdAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	NewRun
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
