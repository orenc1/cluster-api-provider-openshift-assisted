package failuredomains

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFailureDomains(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Failure Domains Suite")
}
