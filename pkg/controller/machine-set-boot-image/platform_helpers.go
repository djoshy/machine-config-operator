package machineset

import (
	"fmt"
	"strings"

	"github.com/coreos/stream-metadata-go/stream"
	corev1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
)

// This function calls the appropriate reconcile function based on the infra type
// On success, it will return a bool indicating if a patch is required, and an updated
// machineset object if any. It will return an error if any of the above steps fail.
func checkMachineSet(infra *osconfigv1.Infrastructure, machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string, secretClient clientset.Interface) (bool, *machinev1beta1.MachineSet, error) {
	switch infra.Status.PlatformStatus.Type {
	case osconfigv1.AWSPlatformType:
		return reconcileAWS(machineSet, configMap, arch, secretClient)
	case osconfigv1.AzurePlatformType:
		return reconcileAzure(machineSet, configMap, arch)
	case osconfigv1.GCPPlatformType:
		return reconcileGCP(machineSet, configMap, arch, secretClient)
	case osconfigv1.VSpherePlatformType:
		return reconcileVSphere(machineSet, configMap, arch)
	default:
		klog.Infof("Skipping machineset %s, unsupported platform %s", machineSet.Name, infra.Status.PlatformStatus.Type)
		return false, nil, nil
	}
}

// GCP reconciliation function. Key points:
// -GCP images aren't region specific
// -GCPMachineProviderSpec.Disk(s) stores actual bootimage URL
// -identical for x86_64/amd64 and aarch64/arm64
func reconcileGCP(machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string, secretClient clientset.Interface) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Reconciling MAPI machineset %s on GCP, with arch %s", machineSet.Name, arch)

	// First, unmarshal the GCP providerSpec
	providerSpec := new(machinev1beta1.GCPMachineProviderSpec)
	if err := unmarshalProviderSpec(machineSet, providerSpec); err != nil {
		return false, nil, err
	}

	// Reconcile the GCP provider spec
	patchRequired, newProviderSpec, err := reconcileGCPProviderSpec(configMap, arch, providerSpec, machineSet.Name, secretClient)
	if err != nil {
		return false, nil, err
	}

	// If no patch is required, exit early
	if !patchRequired {
		return false, nil, err
	}

	// If patch is required, marshal the new providerspec into the machineset
	newMachineSet = machineSet.DeepCopy()
	if err := marshalProviderSpec(newMachineSet, newProviderSpec); err != nil {
		return false, nil, err
	}
	return patchRequired, newMachineSet, nil
}

// reconcileGCPProviderSpec reconciles the GCP provider spec by updating boot images
// Returns whether a patch is required, the updated provider spec, and any error
func reconcileGCPProviderSpec(configMap *corev1.ConfigMap, arch string, providerSpec *machinev1beta1.GCPMachineProviderSpec, machineSetName string, secretClient clientset.Interface) (bool, *machinev1beta1.GCPMachineProviderSpec, error) {
	// Next, unmarshal the configmap into a stream object
	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, nil, err
	}

	// Construct the new target bootimage from the configmap
	// This formatting is based on how the installer constructs
	// the boot image during cluster bootstrap
	newBootImage := fmt.Sprintf("projects/%s/global/images/%s", streamData.Architectures[arch].Images.Gcp.Project, streamData.Architectures[arch].Images.Gcp.Name)

	// Grab what the current bootimage is, compare to the newBootImage
	// There is typically only one element in this Disk array, assume multiple to be safe
	patchRequired := false
	newProviderSpec := providerSpec.DeepCopy()
	for idx, disk := range newProviderSpec.Disks {
		// Do not update non-boot disks
		if !disk.Boot {
			continue
		}
		// Nothing to update on a match
		if newBootImage == disk.Image {
			continue
		}
		klog.Infof("New target boot image: %s", newBootImage)
		klog.Infof("Current image: %s", disk.Image)
		// If image does not start with "projects/rhcos-cloud/global/images", this is a custom boot image.
		if !strings.HasPrefix(disk.Image, "projects/rhcos-cloud/global/images") {
			klog.Infof("current boot image %s is unknown, skipping update of ControlPlaneMachineSet %s", disk.Image, machineSetName)
			return false, nil, nil
		}
		patchRequired = true
		newProviderSpec.Disks[idx].Image = newBootImage
	}

	if patchRequired {
		// Ensure the ignition stub is the minimum acceptable spec required for boot image updates
		if err := upgradeStubIgnitionIfRequired(providerSpec.UserDataSecret.Name, secretClient); err != nil {
			return false, nil, err
		}
	}

	return patchRequired, newProviderSpec, nil
}

func reconcileAWS(machineSet *machinev1beta1.MachineSet, configMap *corev1.ConfigMap, arch string, secretClient clientset.Interface) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {

	klog.Infof("Reconciling MAPI machineset %s on AWS, with arch %s", machineSet.Name, arch)

	// First, unmarshal the AWS providerSpec
	providerSpec := new(machinev1beta1.AWSMachineProviderConfig)
	if err := unmarshalProviderSpec(machineSet, providerSpec); err != nil {
		return false, nil, err
	}

	// Reconcile the AWS provider spec
	patchRequired, newProviderSpec, err := reconcileAWSProviderSpec(configMap, arch, providerSpec, machineSet.Name, secretClient)
	if err != nil {
		return false, nil, err
	}

	// If no patch is required, exit early
	if !patchRequired {
		return false, nil, nil
	}

	// If patch is required, marshal the new providerspec into the machineset
	newMachineSet = machineSet.DeepCopy()
	if err := marshalProviderSpec(newMachineSet, newProviderSpec); err != nil {
		return false, nil, err
	}
	return patchRequired, newMachineSet, nil
}

// reconcileAWSProviderSpec reconciles the AWS provider spec by updating AMIs
// Returns whether a patch is required, the updated provider spec, and any error
func reconcileAWSProviderSpec(configMap *corev1.ConfigMap, arch string, providerSpec *machinev1beta1.AWSMachineProviderConfig, machineSetName string, secretClient clientset.Interface) (bool, *machinev1beta1.AWSMachineProviderConfig, error) {
	// Next, unmarshal the configmap into a stream object
	streamData := new(stream.Stream)
	if err := unmarshalStreamDataConfigMap(configMap, streamData); err != nil {
		return false, nil, err
	}
	// Extract the region from the Placement field
	region := providerSpec.Placement.Region

	// Use the GetAwsRegionImage function to find the correct AMI for the region and architecture
	awsRegionImage, err := streamData.GetAwsRegionImage(arch, region)
	if err != nil {
		// On a region not found error, log and skip this MachineSet
		klog.Infof("failed to get AMI for region %s: %v, skipping update of MachineSet %s", region, err, machineSetName)
		return false, nil, nil
	}

	newami := awsRegionImage.Image

	// Perform rest of bootimage logic here
	patchRequired := false
	newProviderSpec := providerSpec.DeepCopy()

	// If the MachineSet does not use an AMI ID, this is unsupported, log and skip the MachineSet
	// This happens when the installer has copied an AMI at install-time
	// Related bug: https://issues.redhat.com/browse/OCPBUGS-57506
	if newProviderSpec.AMI.ID == nil {
		klog.Infof("current AMI.ID is undefined, skipping update of MachineSet %s", machineSetName)
		return false, nil, nil
	}

	currentAMI := *newProviderSpec.AMI.ID

	if newami != currentAMI {
		// Validate that we're allowed to update from the current AMI
		if !AllowedAMIs.Has(currentAMI) {
			klog.Infof("current AMI %s is unknown, skipping update of MachineSet %s", currentAMI, machineSetName)
			return false, nil, nil
		}

		klog.Infof("New target boot image: %s: %s", region, newami)
		klog.Infof("Current image: %s: %s", region, currentAMI)
		patchRequired = true
		// Only one of ID, ARN or Filters in the AMI may be specified, so define
		// a new AMI object with only an ID field.
		newProviderSpec.AMI = machinev1beta1.AWSResourceReference{
			ID: &newami,
		}
	}

	if patchRequired {
		// Ensure the ignition stub is the minimum acceptable spec required for boot image updates
		if err := upgradeStubIgnitionIfRequired(providerSpec.UserDataSecret.Name, secretClient); err != nil {
			return false, nil, err
		}
	}

	return patchRequired, newProviderSpec, nil
}

func reconcileAzure(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Azure with %s arch", machineSet.Name, arch)
	return false, nil, nil
}

func reconcileVSphere(machineSet *machinev1beta1.MachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1beta1.MachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Vsphere with %s arch", machineSet.Name, arch)
	return false, nil, nil
}
