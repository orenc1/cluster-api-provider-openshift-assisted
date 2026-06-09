/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kubevirt

import (
	"fmt"
	"strings"
)

const (
	DNSProxyConfigMapName  = "kubevirt-dns-proxy-manifests"
	TenantDNSFwdConfigName = "kubevirt-tenant-dns-fwd-manifests"
)

// GenerateDNSProxyManifests generates the manifests for the DNS proxy DaemonSet
// that runs on the infra cluster to resolve tenant cluster domains (api, api-int, *.apps).
// This is the zero-IP variant that creates a forward-only DNS proxy.
func GenerateDNSProxyManifests(
	clusterName string,
	baseDomain string,
	namespace string,
	infraClusterDNSServiceIP string,
) []ManifestEntry {
	return GenerateDNSProxyManifestsWithIPs(clusterName, baseDomain, namespace, infraClusterDNSServiceIP, nil, nil)
}

// GenerateDNSProxyManifestsWithIPs generates the manifests for the DNS proxy DaemonSet
// that runs on the infra cluster to resolve tenant cluster domains (api, api-int, *.apps).
//
// When apiIPs are provided (typically the API service ClusterIP), the DNS proxy resolves
// api and api-int to those IPs. This is critical for the MCS (Machine Config Server)
// during installation: non-bootstrap nodes resolve api-int to the service ClusterIP,
// connect on port 22623, and the service routes to the bootstrap node running MCS.
//
// Lessons learned from production debugging:
// - CoreDNS template plugin wildcard entries MUST use Go template syntax {{ .Name }}
//   (NOT {[.Name]} which produces malformed DNS responses that resolvers silently ignore)
// - The dns-proxy pods must use the infra cluster DNS service IP (typically 172.30.0.10)
//   as their upstream, not node IPs (which may be unreachable from pods in bridge mode)
// - api-int MUST resolve to the API service ClusterIP (not individual VM IPs) to ensure
//   MCS traffic reaches the bootstrap node during installation via Kubernetes service routing
func GenerateDNSProxyManifestsWithIPs(
	clusterName string,
	baseDomain string,
	namespace string,
	infraClusterDNSServiceIP string,
	apiIPs []string,
	ingressIPs []string,
) []ManifestEntry {
	fqdn := fmt.Sprintf("%s.%s", clusterName, baseDomain)

	var manifests []ManifestEntry

	corefile := generateCorefile(fqdn, infraClusterDNSServiceIP, apiIPs, ingressIPs)
	manifests = append(manifests, ManifestEntry{
		Filename: "01-dns-proxy-configmap.yaml",
		Content: fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: tenant-dns-corefile
  namespace: %s
data:
  Corefile: |
%s
`, namespace, indentMultiline(corefile, 4)),
	})

	// DNS proxy DaemonSet
	manifests = append(manifests, ManifestEntry{
		Filename: "02-dns-proxy-daemonset.yaml",
		Content: fmt.Sprintf(`apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: tenant-dns-proxy
  namespace: %s
  labels:
    app: tenant-dns-proxy
spec:
  selector:
    matchLabels:
      app: tenant-dns-proxy
  template:
    metadata:
      labels:
        app: tenant-dns-proxy
    spec:
      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
      tolerations:
        - operator: Exists
      containers:
        - name: coredns
          image: registry.k8s.io/coredns/coredns:v1.11.1
          args: ["-conf", "/etc/coredns/Corefile"]
          ports:
            - containerPort: 5353
              protocol: UDP
              hostPort: 5353
            - containerPort: 5353
              protocol: TCP
              hostPort: 5353
          volumeMounts:
            - name: config
              mountPath: /etc/coredns
              readOnly: true
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              memory: 64Mi
      volumes:
        - name: config
          configMap:
            name: tenant-dns-corefile
`, namespace),
	})

	return manifests
}

// generateCorefile generates a CoreDNS Corefile for the DNS proxy.
//
// CRITICAL: The template plugin entries for wildcard *.apps resolution MUST use
// the correct Go template syntax: {{ .Name }}
// Using {[.Name]} or other incorrect syntax produces responses that resolvers
// silently discard, causing hours of debugging.
//
// apiIPs: IP addresses of control plane nodes (for api/api-int records)
// ingressIPs: IP addresses for ingress (*.apps records)
func generateCorefile(fqdn string, upstreamDNS string, apiIPs []string, ingressIPs []string) string {
	escapedFQDN := strings.ReplaceAll(fqdn, ".", "[.]")

	var apiAnswers, ingressAnswers string
	for _, ip := range apiIPs {
		apiAnswers += fmt.Sprintf("        answer \"api.%s 60 IN A %s\"\n", fqdn, ip)
	}
	for _, ip := range ingressIPs {
		// CRITICAL: Use {{ .Name }} for wildcard template responses.
		// Do NOT use {[.Name]} - it will produce malformed DNS responses
		// that resolvers silently discard without any error message.
		ingressAnswers += fmt.Sprintf("        answer \"{{ .Name }} 60 IN A %s\"\n", ip)
	}

	// If no IPs are known yet, use forward-only mode
	if len(apiIPs) == 0 && len(ingressIPs) == 0 {
		return fmt.Sprintf(`%s:5353 {
    errors
    log

    template IN AAAA {
        match ".*"
        rcode NOERROR
    }

    template IN ANY {
        match ".*"
        rcode NOERROR
    }

    forward . %s
    cache 30
}
`, fqdn, upstreamDNS)
	}

	var apiIntAnswers string
	for _, ip := range apiIPs {
		apiIntAnswers += fmt.Sprintf("        answer \"api-int.%s 60 IN A %s\"\n", fqdn, ip)
	}

	return fmt.Sprintf(`%s:5353 {
    errors
    log

    template IN A api.%s {
        match "^api[.]%s[.]$"
%s        fallthrough
    }

    template IN A api-int.%s {
        match "^api-int[.]%s[.]$"
%s        fallthrough
    }

    template IN A {
        match ".*[.]apps[.]%s[.]$"
%s        fallthrough
    }

    template IN AAAA {
        match ".*"
        rcode NOERROR
    }

    template IN ANY {
        match ".*"
        rcode NOERROR
    }

    forward . %s
    cache 30
}
`, fqdn, fqdn, escapedFQDN, apiAnswers, fqdn, escapedFQDN, apiIntAnswers, escapedFQDN, ingressAnswers, upstreamDNS)
}

// GenerateTenantDNSForwarderManifests generates a day-0 manifest that configures
// the tenant cluster's DNS operator to forward queries for the tenant's own FQDN
// (clusterName.baseDomain) back to the infra cluster's DNS.
//
// The zone MUST be the full FQDN (e.g., "kubevirt-tenant.apps.example.com"),
// NOT just the baseDomain — otherwise the DNS operator won't match queries for
// api.kubevirt-tenant.apps.example.com or *.apps.kubevirt-tenant.apps.example.com.
//
// In KubeVirt deployments, the tenant VMs are pods on the infra cluster's network
// and can reach the infra cluster's DNS service ClusterIP (172.30.0.10). Forwarding
// to this IP works because the infra DNS operator has a forwarding rule (configured
// by EnsureDNSProxy) that routes tenant domain queries to the tenant-dns proxy.
//
// This ConfigMap MUST always be created (even before node IPs are known) because
// it is referenced in AgentClusterInstall.spec.manifestsConfigMapRefs. If the
// ConfigMap doesn't exist, AgentClusterInstall reports SpecSynced: False and
// the installation cannot proceed.
func GenerateTenantDNSForwarderManifests(fqdn string, dnsProxyNodeIPs []string) []ManifestEntry {
	if len(dnsProxyNodeIPs) == 0 {
		// Forward to the infra cluster's DNS service. This works for KubeVirt
		// because tenant VMs can reach infra ClusterIPs directly (they are pods
		// on the infra network). The infra DNS has a forwarding rule that routes
		// these queries to the tenant-dns proxy.
		return []ManifestEntry{
			{
				Filename: "99-tenant-dns-forwarder.yaml",
				Content: fmt.Sprintf(`apiVersion: operator.openshift.io/v1
kind: DNS
metadata:
  name: default
spec:
  servers:
    - name: infra-domain-forwarder
      zones:
        - "%s"
      forwardPlugin:
        upstreams:
          - "172.30.0.10:53"
        policy: Random
`, fqdn),
			},
		}
	}

	upstreams := ""
	for _, ip := range dnsProxyNodeIPs {
		upstreams += fmt.Sprintf("          - \"%s:5353\"\n", ip)
	}

	return []ManifestEntry{
		{
			Filename: "99-tenant-dns-forwarder.yaml",
			Content: fmt.Sprintf(`apiVersion: operator.openshift.io/v1
kind: DNS
metadata:
  name: default
spec:
  servers:
    - name: infra-domain-forwarder
      zones:
        - "%s"
      forwardPlugin:
        upstreams:
%s        policy: Random
`, fqdn, upstreams),
		},
	}
}
