// Package registry — identity helpers for control-plane registration.
//
// On Kubernetes the StatefulSet pod name doubles as a DNS-resolvable headless
// service endpoint, so the bare hostname is sufficient as the cluster ID that
// the registry returns in the x-target-cluster header. Outside Kubernetes,
// multiple control-plane binaries (one per Envoy version) can run on the same
// host with distinct ports — they need distinct, DNS-friendly IDs that the
// operator maps to the host IP in /etc/hosts and references in the Envoy
// cluster config.
package registry

import (
	"fmt"
	"os"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
)

// IsKubernetes reports whether the binary is running inside a Kubernetes pod.
func IsKubernetes() bool {
	return os.Getenv("KUBERNETES_SERVICE_PORT") != ""
}

func cleanHostname() string {
	h, _ := os.Hostname()
	if h == "" {
		h = "unknown"
	}
	return strings.Split(h, ".")[0]
}

// cleanVersion strips a leading "v" from build-time injected strings such as
// "v1.38.0" so the resulting ID is "<host>-controlplane-1.38.0" rather than
// "<host>-controlplane-v1.38.0".
func cleanVersion(v string) string {
	if v == "" {
		return "unknown"
	}
	return strings.TrimPrefix(v, "v")
}

// ResolveControllerID returns the identity advertised by this controller
// instance to the registry. Used for x-target-cluster routing — Envoy
// matches it against a cluster of the same name.
//
// Resolution order:
//  1. cfg.ControllerID (explicit override)
//  2. Kubernetes:     hostname
//  3. Non-Kubernetes: "<hostname>-controller"
//
// The non-K8s suffix is intentionally version-agnostic: each host runs a
// single controller (the multi-version layer lives in control-plane), and
// the standalone deployment's /etc/hosts plus Envoy bootstrap reference the
// "<hostname>-controller" form.
func ResolveControllerID(cfg *config.AppConfig) string {
	if cfg != nil && cfg.ControllerID != "" {
		return cfg.ControllerID
	}
	h := cleanHostname()
	if IsKubernetes() {
		return h
	}
	return fmt.Sprintf("%s-controller", h)
}

// ResolveControlPlaneID returns the identity advertised by this control-plane
// instance to the registry.
//
// Resolution order:
//  1. cfg.ControlPlaneID (explicit override)
//  2. Kubernetes:     hostname
//  3. Non-Kubernetes: "<hostname>-controlplane-<version>"
func ResolveControlPlaneID(cfg *config.AppConfig, version string) string {
	if cfg != nil && cfg.ControlPlaneID != "" {
		return cfg.ControlPlaneID
	}
	h := cleanHostname()
	if IsKubernetes() {
		return h
	}
	return fmt.Sprintf("%s-controlplane-%s", h, cleanVersion(version))
}

// ResolveControllerHTTPAddress returns the host:port that other components
// (cross-controller forwarder, registry storage) use to reach a controller's
// REST endpoint.
//
//   Kubernetes:     "<pod>.<svc>-headless.<ns>.svc:<port>" (StatefulSet DNS)
//   Non-Kubernetes: "<controllerID>:<port>"                (resolved via the
//                                                           operator's /etc/hosts
//                                                           or real DNS)
//
// port==0 falls back to 8099 to preserve the historical default.
func ResolveControllerHTTPAddress(controllerID string, port uint, namespace string) string {
	if port == 0 {
		port = 8099
	}
	if IsKubernetes() {
		return fmt.Sprintf("%s:%d", helper.ToK8sServiceName(controllerID, namespace), port)
	}
	return fmt.Sprintf("%s:%d", controllerID, port)
}
