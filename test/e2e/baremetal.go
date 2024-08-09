package e2e

import (
	"context"
	"fmt"

	"libvirt.org/go/libvirt"

	"os/exec"
	"strings"

	"github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func CleanupSushyTools(username, host string) error {
	dockerRunCmd := `podman rm -f sushy-tools`
	cmd := exec.Command("ssh", "-A", fmt.Sprintf("%s@%s", username, host), dockerRunCmd)
	_, err := cmd.CombinedOutput()
	return err
}

func SetupSushyTools(username, host string) error {
	sushyConf := `
# Listen on 192.168.222.1:8000
SUSHY_EMULATOR_LISTEN_IP = u"192.168.222.1"
SUSHY_EMULATOR_LISTEN_PORT = 8000
# The libvirt URI to use. This option enables libvirt driver.
SUSHY_EMULATOR_LIBVIRT_URI = u"qemu:///system"
SUSHY_EMULATOR_IGNORE_BOOT_DEVICE = True
`
	sushyToolsConfPath := "/tmp/sushy-emulator.conf"
	dockerRunCmd := fmt.Sprintf(`podman run --name sushy-tools --rm --network host --privileged -d \
	  -v /var/run/libvirt:/var/run/libvirt:z \
	  -v "%s:/etc/sushy/sushy-emulator.conf:z" \
	  -e SUSHY_EMULATOR_CONFIG=/etc/sushy/sushy-emulator.conf \
	  quay.io/metal3-io/sushy-tools:latest sushy-emulator
`, sushyToolsConfPath)
	createConfigCmd := fmt.Sprintf("echo '%s' > %s; ", sushyConf, sushyToolsConfPath)

	cmd := exec.Command("ssh", "-A", fmt.Sprintf("%s@%s", username, host), createConfigCmd)
	_, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred())

	if err != nil {
		return err
	}
	Expect(err).NotTo(HaveOccurred())

	cmd = exec.Command("ssh", "-A", fmt.Sprintf("%s@%s", username, host), dockerRunCmd)
	out, err := cmd.CombinedOutput()

	if err != nil {
		Expect(string(out)).To(BeEmpty())
		return err
	}
	Expect(err).NotTo(HaveOccurred())
	return cmd.Err
}

func getSystemName(username, host, id string) (string, error) {
	getHostNameCmd := fmt.Sprintf(`curl -s 192.168.222.1:8000%s | jq -r '.Name'`, id)
	return remoteExec(username, host, getHostNameCmd)
}

func getRedfishIDs(username, host string) ([]string, error) {
	getRedfishIDsCmd := `curl -s 192.168.222.1:8000/redfish/v1/Systems | jq -r '.Members[]."@odata.id"'`
	out, err := remoteExec(username, host, getRedfishIDsCmd)
	ids := strings.Split(out, "\n")
	return ids, err
}

func CreateBMH(ctx context.Context, client client.Client, namespace, name, macAddress, systemID string) error {
	secretName := fmt.Sprintf("%s-secret", name)
	bmh := v1alpha1.BareMetalHost{
		TypeMeta: metav1.TypeMeta{
			Kind:       "BareMetalHost",
			APIVersion: "metal3.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"inspect.metal3.io": "disabled",
			},
		},
		Spec: v1alpha1.BareMetalHostSpec{
			BMC: v1alpha1.BMCDetails{
				Address:                        fmt.Sprintf("redfish-virtualmedia+http://192.168.222.1:8000%s", systemID),
				CredentialsName:                secretName,
				DisableCertificateVerification: false,
			},
			BootMode:       "UEFI",
			BootMACAddress: macAddress,
			Online:         true,
		},
	}
	if err := client.Create(ctx, &bmh); err != nil {
		return err
	}
	secret := v1.Secret{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		StringData: map[string]string{
			"username": "YWRtaW4=",
			"password": "cGFzc3dvcmQ=",
		},
		Type: "Opaque",
	}
	return client.Create(ctx, &secret)
}

func CreateDomain(username, host, domainName, macAddress string) (string, error) {
	createDomainCmd := fmt.Sprintf(
		//nolint:all
		"virt-install -n \"%s\" --pxe --os-variant=rhel8.0 --ram=16384 --vcpus=8 --network network=bmh,mac=\"%s\" --disk size=120,bus=scsi,sparse=yes --check disk_size=off --noautoconsole",
		domainName,
		macAddress,
	)

	return remoteExec(username, host, createDomainCmd)
}

func CleanupDomains(conn *libvirt.Connect) error {
	domains, err := conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_ACTIVE | libvirt.CONNECT_LIST_DOMAINS_INACTIVE)
	if err != nil {
		return err
	}
	for _, domain := range domains {
		domainName, err := domain.GetName()
		if err != nil {
			continue
		}
		if strings.HasPrefix(domainName, "bmh-vm") {
			_ = domain.Destroy()
			_ = domain.UndefineFlags(libvirt.DOMAIN_UNDEFINE_NVRAM | libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA)
			_ = domain.Undefine()
		}
	}
	return nil
}

func CreateVMsAndBMHs(
	ctx context.Context,
	k8sClient client.Client,
	username, hostname, namespace string,
	hostNum int,
) error {
	nameToMac := map[string]string{}

	// Setup VMs
	for i, macAddress := range generateMACAddresses(hostNum) {
		domainName := fmt.Sprintf("bmh-vm-%02d", i+1)
		_, err := CreateDomain(username, hostname, domainName, macAddress)
		if err != nil {
			return err
		}
		nameToMac[domainName] = macAddress
	}
	systemIDs, err := getRedfishIDs(username, hostname)
	if err != nil {
		return err
	}

	// Setup BMHs
	for _, systemID := range systemIDs {
		name, err := getSystemName(username, hostname, systemID)
		Expect(err).NotTo(HaveOccurred())
		if macAddress, ok := nameToMac[name]; ok {
			err = CreateBMH(ctx, k8sClient, namespace, name, macAddress, systemID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
