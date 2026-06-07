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
	Path      string      `json:"path"`
	Mode      int         `json:"mode"`
	Overwrite bool        `json:"overwrite"`
	Contents  ignContents `json:"contents"`
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
func KubeVirtDiscoveryIgnitionOverride(sshKey string) (string, error) {
	if sshKey == "" {
		return "", nil
	}

	config := ignitionConfig{
		Ignition: ignitionVersion{Version: "3.1.0"},
		Passwd: &ignPasswd{
			Users: []ignUser{
				{
					Name:              "core",
					SSHAuthorizedKeys: []string{sshKey},
				},
			},
		},
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
	config := ignitionConfig{
		Ignition: ignitionVersion{Version: "3.1.0"},
		Storage: &ignStorage{
			Files: []ignFile{
				{
					Path:      "/etc/resolv.conf",
					Mode:      0644,
					Overwrite: true,
					Contents:  ignContents{Source: dataURL(fmt.Sprintf("nameserver %s\n", infraClusterDNSIP))},
				},
				{
					Path:      "/etc/NetworkManager/conf.d/99-capoa-dns.conf",
					Mode:      0644,
					Overwrite: true,
					Contents:  ignContents{Source: dataURL("[main]\ndns=none\n")},
				},
				{
					Path:      "/etc/gai.conf",
					Mode:      0644,
					Overwrite: true,
					Contents:  ignContents{Source: dataURL("precedence ::ffff:0/0 100\n")},
				},
				{
					Path:      "/opt/openshift/manifests/placeholder.yaml",
					Mode:      0644,
					Overwrite: true,
					Contents:  ignContents{Source: dataURL(placeholderManifest)},
				},
				{
					Path:      "/opt/openshift/openshift/placeholder.yaml",
					Mode:      0644,
					Overwrite: true,
					Contents:  ignContents{Source: dataURL(placeholderManifest)},
				},
			},
		},
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
