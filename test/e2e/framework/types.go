//go:build e2e
// +build e2e

package framework

// PaddockEvent mirrors the serialised PaddockEvent. Decoupled from
// the api module's typed client to keep the test build surface small.
type PaddockEvent struct {
	SchemaVersion string            `json:"schemaVersion"`
	Timestamp     string            `json:"ts"`
	Type          string            `json:"type"`
	Summary       string            `json:"summary,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
}

// HarnessRunStatus mirrors the trimmed subset of HarnessRun status
// that the e2e suite cares about.
type HarnessRunStatus struct {
	Phase        string                `json:"phase"`
	JobName      string                `json:"jobName"`
	WorkspaceRef string                `json:"workspaceRef"`
	RecentEvents []PaddockEvent        `json:"recentEvents"`
	Conditions   []HarnessRunCondition `json:"conditions"`
	Outputs      *struct {
		Summary      string `json:"summary"`
		FilesChanged int    `json:"filesChanged"`
	} `json:"outputs"`
}

type HarnessRunCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// AuditEventList is the kubectl-get-json shape returned for a list of
// AuditEvent CRs.
type AuditEventList struct {
	Items []AuditEvent `json:"items"`
}

// AuditEvent mirrors the trimmed subset of the AuditEvent CRD the
// suite asserts against. Parsed from `kubectl get auditevents -o json`
// so the e2e package doesn't need a typed client.
type AuditEvent struct {
	Metadata struct {
		Name              string `json:"name"`
		Namespace         string `json:"namespace"`
		CreationTimestamp string `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Decision  string `json:"decision"`
		Kind      string `json:"kind"`
		Timestamp string `json:"timestamp"`
		Reason    string `json:"reason"`
		RunRef    *struct {
			Name string `json:"name"`
		} `json:"runRef,omitempty"`
		Destination *struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"destination,omitempty"`
	} `json:"spec"`
}
