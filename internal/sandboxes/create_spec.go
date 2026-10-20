package sandboxes

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// providerLabel is the label key AgentKit reserves for itself, and every key under
// it (providerLabel + "."); a provider refuses a spec that has one.
const providerLabel = "agentkit.sandbox"

// hubLabelPrefix starts every label hub sets on a box.
const hubLabelPrefix = "hub."

// buildSpec is the spec of the box of a lease: the spec hub asks the provider
// for. Everything in it comes from the lease, which snapshots the profile it was
// issued under, except the time left, which is ttl, and the two things the
// caller may choose: its working directory and its labels. The caller's image,
// environment, network, resources, TTL and Landlock setting are not read.
//
// The environment holds the lease's fixed environment only. The secrets are
// added when the box is created.
func buildSpec(lease *Lease, caller sandbox.Spec, caps sandbox.Capabilities, ttl time.Duration) (sandbox.Spec, error) {
	mode, ok := networkMode(caps, lease.Network)
	if !ok {
		return sandbox.Spec{}, fmt.Errorf("network mode %q is not honoured by provider %q", lease.Network, caps.Provider)
	}
	var errs []error
	memory, memoryOK := megabytesToBytes(lease.Resources.MemoryMB)
	if !memoryOK {
		errs = append(errs, fmt.Errorf("memory_mb %d is too large", lease.Resources.MemoryMB))
	}
	disk, diskOK := megabytesToBytes(lease.Resources.DiskMB)
	if !diskOK {
		errs = append(errs, fmt.Errorf("disk_mb %d is too large", lease.Resources.DiskMB))
	}
	if len(errs) > 0 {
		return sandbox.Spec{}, errors.Join(errs...)
	}

	env := make(map[string]string, len(lease.Env))
	for k, v := range lease.Env {
		env[k] = v
	}
	return sandbox.Spec{
		Image:   lease.Image,
		Workdir: caller.Workdir,
		Env:     env,
		Resources: sandbox.Resources{
			CPUs:        lease.Resources.CPUs,
			MemoryBytes: memory,
			PIDs:        lease.Resources.PIDs,
			DiskBytes:   disk,
		},
		Network:         sandbox.Network{Mode: mode},
		TTL:             ttl,
		Labels:          boxLabels(lease, caller.Labels),
		RequireLandlock: lease.RequireLandlock,
	}, nil
}

// boxLabels are the labels of the box: the caller's, except every key hub sets
// itself (hub.*) and the keys the provider reserves, then each label of the
// lease as hub.<key>, and last hub.lease and hub.workspace, which override any
// label before them. A caller cannot name another lease's box as its own.
func boxLabels(lease *Lease, caller map[string]string) map[string]string {
	labels := make(map[string]string, len(caller)+len(lease.Labels)+2)
	for k, v := range caller {
		if strings.HasPrefix(k, hubLabelPrefix) || k == providerLabel || strings.HasPrefix(k, providerLabel+".") {
			continue
		}
		labels[k] = v
	}
	for k, v := range lease.Labels {
		labels[hubLabelPrefix+k] = v
	}
	labels[hubLabelPrefix+"lease"] = lease.ID
	labels[hubLabelPrefix+"workspace"] = lease.Workspace
	return labels
}
