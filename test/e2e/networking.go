package e2e

import (
	"encoding/xml"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	"libvirt.org/go/libvirt"
	libvirtxml "libvirt.org/libvirt-go-xml"
)

const (
	assistedServiceDomain      = "assisted-service.assisted-installer.com"
	assistedImageServiceDomain = "assisted-image.assisted-installer.com"

	networkName = "bmh"
)

func SetDNS(username, host, device, nameservers string) error {
	out, err := remoteExec(username, host, `nmcli con mod "`+device+`" ipv4.dns "`+nameservers+`"`)
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "[DEBUG] error setting DNS servers: %s", out)
		return err
	}
	out, err = remoteExec(username, host, `service NetworkManager restart`)
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "[DEBUG] error restarting NetworkManager: %s", out)
	}
	return err
}

func SetupLibvirtNetwork(conn *libvirt.Connect, ingressIP, nameserver string, hostNum int) (*libvirt.Network, error) {
	network := getNetwork(ingressIP, nameserver, hostNum)
	networkXML, err := network.Marshal()
	if err != nil {
		return nil, err
	}
	return conn.NetworkCreateXML(networkXML)
}

func getNetwork(ingressIP, nameserver string, hostNum int) libvirtxml.Network {
	hosts := []libvirtxml.NetworkDHCPHost{}
	for i, mac := range generateMACAddresses(hostNum) {
		suffix := 30 + i
		host := libvirtxml.NetworkDHCPHost{

			MAC:  mac,
			Name: fmt.Sprintf("okd-%d", i),
			IP:   fmt.Sprintf("192.168.222.%d", suffix),
		}
		hosts = append(hosts, host)
	}
	return libvirtxml.Network{
		Name: networkName,
		Forward: &libvirtxml.NetworkForward{
			Mode: "nat",
		},
		Bridge: &libvirtxml.NetworkBridge{
			Name:  networkName,
			STP:   "on",
			Delay: "0",
		},
		MAC: &libvirtxml.NetworkMAC{Address: "52:54:00:fb:8a:5f"},
		IPs: []libvirtxml.NetworkIP{
			{
				Address: "192.168.222.1",
				Netmask: "255.255.255.0",
				DHCP: &libvirtxml.NetworkDHCP{
					Ranges: []libvirtxml.NetworkDHCPRange{
						{
							Start: "192.168.222.2",
							End:   "192.168.222.254",
						},
					},
					Hosts: hosts,
				},
			},
		},
		DnsmasqOptions: &libvirtxml.NetworkDnsmasqOptions{
			XMLName: xml.Name{},
			Option: []libvirtxml.NetworkDnsmasqOption{
				{Value: "local=/lab.home/"},
				{Value: "address=/api.test-sno.lab.home/192.168.222.31"},
				{Value: "address=/api-int.test-sno.lab.home/192.168.222.31"},
				{Value: "address=/.apps.test-sno.lab.home/192.168.222.31"},
				{Value: "address=/api.test-multinode.lab.home/192.168.222.40"},
				{Value: "address=/api-int.test-multinode.lab.home/192.168.222.40"},
				{Value: "address=/.apps.test-multinode.lab.home/192.168.222.41"},
				{Value: "address=/" + assistedServiceDomain + "/" + ingressIP},
				{Value: "address=/" + assistedImageServiceDomain + "/" + ingressIP},
				{Value: "server=" + nameserver},
			},
		},
	}
}

func CleanNetworks(conn *libvirt.Connect) error {
	nets, err := conn.ListAllNetworks(libvirt.CONNECT_LIST_NETWORKS_ACTIVE | libvirt.CONNECT_LIST_NETWORKS_INACTIVE)
	if err != nil {
		return err
	}
	for _, net := range nets {
		name, err := net.GetName()
		if err != nil {
			return err
		}
		if name == networkName {
			return net.Destroy()
		}
	}
	return nil
}
