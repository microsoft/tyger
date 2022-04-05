package model

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

type Codespec struct {
	Buffers    *BufferParameters  `json:"buffers,omitempty"`
	Image      string             `json:"image"`
	Command    []string           `json:"command,omitempty"`
	Args       []string           `json:"args,omitempty"`
	WorkingDir string             `json:"workingDir,omitempty"`
	Env        map[string]string  `json:"env,omitempty"`
	Resources  *CodespecResources `json:"resources,omitempty"`
}

type Run struct {
	Id            string            `json:"id,omitempty"`
	Buffers       map[string]string `json:"buffers,omitempty"`
	Codespec      string            `json:"codespec"`
	Status        string            `json:"status,omitempty"`
	ComputeTarget *RunComputeTarget `json:"computeTarget,omitempty"`
}

type RunComputeTarget struct {
	Cluster  string `json:"cluster,omitempty"`
	NodePool string `json:"nodePool,omitempty"`
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
