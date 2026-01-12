package extended

import (
	"context"
	"path/filepath"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	osconfigv1 "github.com/openshift/api/config/v1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"

	extpriv "github.com/openshift/machine-config-operator/test/extended-priv"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive][OCPFeatureGate:BootImageSkewEnforcement]", func() {
	defer g.GinkgoRecover()

	var (
		ManualHappyFixture = filepath.Join("machineconfigurations", "skewenforcement-manual-happy.yaml")
		ManualSadFixture   = filepath.Join("machineconfigurations", "skewenforcement-manual-sad.yaml")
		NoneFixture        = filepath.Join("machineconfigurations", "skewenforcement-none.yaml")
		EmptyFixture       = filepath.Join("machineconfigurations", "machineconfigurations-empty.yaml")

		oc = exutil.NewCLI("mco-bootimage", exutil.KubeConfigPath()).AsAdmin()
	)

	g.BeforeEach(func() {
		// Skip on single-node topologies
		skipOnSingleNodeTopology(oc)
	})

	g.AfterEach(func() {
		exutil.By("Resetting MachineConfiguration")
		// Reset MachineConfiguration between tests
		applyMachineConfigurationFixture(oc, EmptyFixture)

	})

	g.It("Verify Manual mode and Upgradeable (Happy case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Set manual mode with a boot image version that is within skew limits
		applyMachineConfigurationFixture(oc, ManualHappyFixture)

		// Check machine-config CO upgradeable status, should be set to true
		upgradeableStatus := extpriv.NewClusterOperator(oc, "machine-config").IsConditionStatusTrue("Upgradeable")
		o.Expect(upgradeableStatus).Should(o.BeTrue())

	})

	g.It("Verify Manual mode and Upgradeable (Sad case) [apigroup:machineconfiguration.openshift.io]", func() {
		// Set manual mode with a boot image version that is NOT within skew limits
		applyMachineConfigurationFixture(oc, ManualSadFixture)

		// Check machine-config CO upgradeable status, should be set to false
		upgradeableStatus := extpriv.NewClusterOperator(oc, "machine-config").IsConditionStatusTrue("Upgradeable")
		o.Expect(upgradeableStatus).Should(o.BeFalse())

	})

	g.It("Verify Automatic mode and Upgradeable (Happy Case) [apigroup:machineconfiguration.openshift.io]", func() {
		// only applicable on GCP, AWS clusters
		skipUnlessTargetPlatform(oc, osconfigv1.GCPPlatformType, osconfigv1.AWSPlatformType)

		// No opinion on skew enforcement for these platforms will result in Automatic mode
		applyMachineConfigurationFixture(oc, EmptyFixture)

		// Check machine-config CO upgradeable status, should be set to true
		upgradeableStatus := extpriv.NewClusterOperator(oc, "machine-config").IsConditionStatusTrue("Upgradeable")
		o.Expect(upgradeableStatus).Should(o.BeTrue())
	})

	g.It("Verify Automatic mode and Upgradeable (Sad Case) [apigroup:machineconfiguration.openshift.io]", func(ctx context.Context) {
		// only applicable on GCP, AWS clusters
		skipUnlessTargetPlatform(oc, osconfigv1.GCPPlatformType, osconfigv1.AWSPlatformType)

		// No opinion on skew enforcement for these platforms will result in Automatic mode
		applyMachineConfigurationFixture(oc, EmptyFixture)

		// Pick a random machineset to test
		machineClient, err := machineclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(err).NotTo(o.HaveOccurred())
		machineSetUnderTest := getRandomMachineSet(machineClient)
		framework.Logf("MachineSet under test: %s", machineSetUnderTest.Name)

		// Set a non-existent user data secret in the machineset's providerSpec; this will cause a boot image controller degrade
		machineSet := extpriv.NewNamespacedResource(oc, MAPIMachinesetQualifiedName, "openshift-machine-api", machineSetUnderTest.Name)
		originalUserDataSecret := machineSet.GetOrFail(`{.spec.template.spec.providerSpec.value.userDataSecret.name}`)
		defer func() {
			// Restore the original user data secret
			err := machineSet.Patch("json", `[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/userDataSecret/name", "value": "`+originalUserDataSecret+`"}]`)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		nonExistentSecret := "non-existent-user-data-secret"
		err = machineSet.Patch("json", `[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/userDataSecret/name", "value": "`+nonExistentSecret+`"}]`)
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("Set non-existent user data secret '%s' in MachineSet %s", nonExistentSecret, machineSetUnderTest.Name)

		// Patch the boot image to an older version to trigger an update loop
		// Detect platform and patch the appropriate boot image field
		infra, err := oc.AdminConfigClient().ConfigV1().Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		var originalBootImage string
		var bootImagePatch string
		var backdatedBootImage string

		switch infra.Status.PlatformStatus.Type {
		case osconfigv1.GCPPlatformType:
			originalBootImage = machineSet.GetOrFail(`{.spec.template.spec.providerSpec.value.disks[0].image}`)
			backdatedBootImage = "projects/rhcos-cloud/global/images/rhcos-410-84-202210040010-0-gcp-x86-64"
			bootImagePatch = `[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/disks/0/image", "value": "` + backdatedBootImage + `"}]`
		case osconfigv1.AWSPlatformType:
			originalBootImage = machineSet.GetOrFail(`{.spec.template.spec.providerSpec.value.ami.id}`)
			backdatedBootImage = "ami-000145e5a91e9ac22"
			bootImagePatch = `[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/ami/id", "value": "` + backdatedBootImage + `"}]`
		}

		defer func() {
			// Restore the original boot image
			var restorePatch string
			switch infra.Status.PlatformStatus.Type {
			case osconfigv1.GCPPlatformType:
				restorePatch = `[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/disks/0/image", "value": "` + originalBootImage + `"}]`
			case osconfigv1.AWSPlatformType:
				restorePatch = `[{"op": "replace", "path": "/spec/template/spec/providerSpec/value/ami/id", "value": "` + originalBootImage + `"}]`
			}
			err := machineSet.Patch("json", restorePatch)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		err = machineSet.Patch("json", bootImagePatch)
		o.Expect(err).NotTo(o.HaveOccurred())
		framework.Logf("Set backdated boot image '%s' in MachineSet %s to trigger update loop", backdatedBootImage, machineSetUnderTest.Name)

		// Check machine-config CO upgradeable status, should be set to false due to the degrade
		mco := extpriv.NewClusterOperator(oc, "machine-config")
		o.Eventually(func() bool {
			return mco.IsConditionStatusTrue("Upgradeable")
		}, "2m", "10s").Should(o.BeFalse(), "co/machine-config should not be upgradeable with broken machineset and update loop")
	})

	g.It("Verify None mode [apigroup:machineconfiguration.openshift.io]", func() {

		// Set None mode, effectviely disabling skew enforcement
		applyMachineConfigurationFixture(oc, NoneFixture)

		// Check machine-config CO upgradeable status, should be set to true
		upgradeableStatus := extpriv.NewClusterOperator(oc, "machine-config").IsConditionStatusTrue("Upgradeable")
		o.Expect(upgradeableStatus).Should(o.BeTrue())

	})
})
