package e2e

import (
	"fmt"
	"os/exec"
	"strings"
)

func remoteExec(username, host, command string) (string, error) {
	cmd := exec.Command("ssh", "-A", fmt.Sprintf("%s@%s", username, host), command)
	out, err := cmd.CombinedOutput()
	stringOutput := strings.Trim(string(out), "\n")
	return stringOutput, err
}
func generateMACAddresses(num int) []string {
	macs := []string{}
	for i := 0; i < num; i++ {
		macs = append(macs, fmt.Sprintf("00:60:2f:31:81:%02d", i))
	}
	return macs
}
