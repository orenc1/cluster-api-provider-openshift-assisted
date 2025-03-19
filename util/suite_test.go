package util_test

import (
	"testing"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"

	v1 "k8s.io/api/apps/v1"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestUtil(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Util Suite")
}

var testScheme = runtime.NewScheme()

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	utilruntime.Must(v1.AddToScheme(testScheme))
})
