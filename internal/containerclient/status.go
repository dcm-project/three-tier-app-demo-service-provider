package containerclient

import (
	"strings"

	"github.com/dcm-project/3-tier-demo-service-provider/api/v1alpha1"
	k8sapi "github.com/dcm-project/k8s-container-service-provider/api/v1alpha1"
)

// WorstStatusFromPodmanStates returns the worst status given 3 Podman container states.
// States: "running", "exited", "created", "paused", "dead", "removing", etc.
// Order: FAILED > PENDING > RUNNING. Returns FAILED if len(states) != 3.
func WorstStatusFromPodmanStates(states []string) (v1alpha1.ThreeTierAppStatus, bool) {
	if len(states) != 3 {
		return v1alpha1.FAILED, true
	}
	worst := v1alpha1.RUNNING
	for _, state := range states {
		switch strings.TrimSpace(strings.ToLower(state)) {
		case "running":
			// keep current worst
		case "created", "paused":
			if worst == v1alpha1.RUNNING {
				worst = v1alpha1.PENDING
			}
		default:
			// exited, dead, removing, etc.
			return v1alpha1.FAILED, true
		}
	}
	return worst, true
}

// AggregateK8sContainerStatuses maps three k8s container instance statuses to a stack status.
// Order: FAILED (any FAILED/DELETED) > PENDING (any PENDING/UNKNOWN) > RUNNING (all RUNNING).
func AggregateK8sContainerStatuses(statuses []k8sapi.ContainerStatus) (v1alpha1.ThreeTierAppStatus, bool) {
	if len(statuses) != 3 {
		return v1alpha1.FAILED, true
	}
	for _, s := range statuses {
		switch s {
		case k8sapi.FAILED, k8sapi.DELETED:
			return v1alpha1.FAILED, true
		}
	}
	for _, s := range statuses {
		switch s {
		case k8sapi.PENDING, k8sapi.UNKNOWN:
			return v1alpha1.PENDING, true
		}
	}
	for _, s := range statuses {
		if s != k8sapi.RUNNING {
			return v1alpha1.PENDING, true
		}
	}
	return v1alpha1.RUNNING, true
}
