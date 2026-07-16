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
	"encoding/base64"
	"encoding/json"
	"fmt"
)

const (
	// Default DNS server IP for the infra cluster (OpenShift CoreDNS service ClusterIP)
	infraClusterDNSIP = "172.30.0.10"
)

// ignitionConfig represents a minimal Ignition v3.1.0 config structure.
type ignitionConfig struct {
	Ignition ignitionVersion `json:"ignition"`
	Passwd   *ignPasswd      `json:"passwd,omitempty"`
	Storage  *ignStorage     `json:"storage,omitempty"`
	Systemd  *ignSystemd     `json:"systemd,omitempty"`
}

type ignSystemd struct {
	Units []ignUnit `json:"units"`
}

type ignUnit struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Contents string `json:"contents"`
}

type ignitionVersion struct {
	Version string `json:"version"`
}

type ignPasswd struct {
	Users []ignUser `json:"users"`
}

type ignUser struct {
	Name              string   `json:"name"`
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys"`
}

type ignStorage struct {
	Files []ignFile `json:"files"`
}

type ignFile struct {
	Path      string        `json:"path"`
	Mode      int           `json:"mode"`
	Overwrite bool          `json:"overwrite,omitempty"`
	Contents  *ignContents  `json:"contents,omitempty"`
	Append    []ignContents `json:"append,omitempty"`
}

type ignContents struct {
	Source string `json:"source"`
}

func dataURL(content string) string {
	return "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte(content))
}

// KubeVirtDiscoveryIgnitionOverride generates the discovery-phase ignition override
// for KubeVirt platform. This adds the SSH key to the discovery ISO so users can
// SSH into VMs during the discovery/boot phase.
func KubeVirtDiscoveryIgnitionOverride(sshKey, dnsProxyIP, clusterName, baseDomain string) (string, error) {
	config := ignitionConfig{
		Ignition: ignitionVersion{Version: "3.1.0"},
	}

	if sshKey != "" {
		config.Passwd = &ignPasswd{
			Users: []ignUser{{Name: "core", SSHAuthorizedKeys: []string{sshKey}}},
		}
	}

	if dnsProxyIP != "" && clusterName != "" && baseDomain != "" {
		apiIntHostname := fmt.Sprintf("api-int.%s.%s", clusterName, baseDomain)
		apiHostname := fmt.Sprintf("api.%s.%s", clusterName, baseDomain)

		dnsResolveScript := fmt.Sprintf(`#!/usr/bin/env python3
import socket, struct, sys, time
def resolve(name, server, port):
    qname = b''
    for p in name.split('.'):
        qname += struct.pack('B', len(p)) + p.encode()
    qname += b'\x00'
    q = struct.pack('>HHHHHH', 0x1234, 0x0100, 1, 0, 0, 0) + qname + struct.pack('>HH', 1, 1)
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(5)
    s.sendto(q, (server, port))
    d, _ = s.recvfrom(512)
    i = 12
    while d[i] != 0: i += 1
    i += 5
    if len(d) > i + 10:
        i += 10
        return '.'.join(str(b) for b in d[i:i+4])
    return None
for attempt in range(30):
    try:
        ip = resolve('%s', '%s', %d)
        if ip:
            with open('/etc/hosts', 'r') as f: lines = [l for l in f if '%s' not in l]
            with open('/etc/hosts', 'w') as f: f.writelines(lines); f.write(f'{ip} %s %s\n')
            print(f'Resolved %s to {ip}')
            sys.exit(0)
    except: pass
    time.sleep(2)
print('WARNING: Could not resolve %s')
sys.exit(1)
`, apiIntHostname, dnsProxyIP, TenantDNSPort,
			apiIntHostname, apiIntHostname, apiHostname,
			apiIntHostname, apiIntHostname)

		config.Storage = &ignStorage{
			Files: []ignFile{
				{Path: "/etc/resolv.conf", Mode: 0644, Overwrite: true, Contents: &ignContents{Source: dataURL(fmt.Sprintf("nameserver %s\nsearch cluster.local svc.cluster.local\noptions ndots:5\n", infraClusterDNSIP))}},
				{Path: "/etc/NetworkManager/conf.d/99-capoa-dns.conf", Mode: 0644, Overwrite: true, Contents: &ignContents{Source: dataURL("[main]\ndns=none\n")}},
				{Path: "/usr/local/bin/capoa-dns-resolve", Mode: 0755, Contents: &ignContents{Source: dataURL(dnsResolveScript)}},
			},
		}
		config.Systemd = &ignSystemd{
			Units: []ignUnit{{
				Name:    "capoa-dns-resolve.service",
				Enabled: true,
				Contents: `[Unit]
Description=Resolve api-int via DNS proxy for KubeVirt pod-networking
Before=kubelet.service crio.service bootkube.service
After=NetworkManager-wait-online.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/capoa-dns-resolve

[Install]
WantedBy=multi-user.target
`,
			}},
		}
	}

	if config.Passwd == nil && config.Storage == nil {
		return "", nil
	}

	data, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("failed to marshal discovery ignition override: %w", err)
	}
	return string(data), nil
}

// KubeVirtInstallIgnitionOverride generates the install-time ignition override for
// KubeVirt platform. This configures:
//   - SSH key for core user
//   - DNS resolution pointing to infra cluster's CoreDNS
//   - NetworkManager configured to not override /etc/resolv.conf
//   - IPv4 preference over IPv6 (avoids AAAA lookup failures in dual-stack)
//   - Placeholder manifests to prevent bootkube crash on empty manifest dirs
func KubeVirtInstallIgnitionOverride(sshKey string) (string, error) {
	// Install ignition: only include files that don't conflict with the MCS-served config.
	// DNS resolution for api-int is handled by the DNS forwarding rule (CoreDNS -> DNS proxy),
	// so no /etc/resolv.conf, /etc/hosts, or NetworkManager overrides are needed here.
	files := []ignFile{
		{Path: "/opt/openshift/manifests/placeholder.yaml", Mode: 0644, Overwrite: true, Contents: &ignContents{Source: dataURL(placeholderManifest)}},
		{Path: "/opt/openshift/openshift/placeholder.yaml", Mode: 0644, Overwrite: true, Contents: &ignContents{Source: dataURL(placeholderManifest)}},
	}

	config := ignitionConfig{
		Ignition: ignitionVersion{Version: "3.1.0"},
		Storage:  &ignStorage{Files: files},
	}

	if sshKey != "" {
		config.Passwd = &ignPasswd{
			Users: []ignUser{
				{
					Name:              "core",
					SSHAuthorizedKeys: []string{sshKey},
				},
			},
		}
	}

	data, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("failed to marshal install ignition override: %w", err)
	}
	return string(data), nil
}

const placeholderManifest = `apiVersion: v1
kind: ConfigMap
metadata:
  name: placeholder-fix
  namespace: default
`
