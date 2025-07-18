package machineset

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	archtranslater "github.com/coreos/stream-metadata-go/arch"
	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1 "github.com/openshift/api/machine/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	opv1 "github.com/openshift/api/operator/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	operatorversion "github.com/openshift/machine-config-operator/pkg/version"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// syncControlPlaneMachineSets will attempt to enqueue every control plane machineset
func (ctrl *Controller) syncControlPlaneMachineSets(reason string) {

	ctrl.cpmsSyncMutex.Lock()
	defer ctrl.cpmsSyncMutex.Unlock()

	var mcop *opv1.MachineConfiguration
	var pollError error
	// Wait for mcop.Status to populate, otherwise error out. This shouldn't take very long
	// as this is done by the operator sync loop.
	if err := wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, 2*time.Minute, true, func(_ context.Context) (bool, error) {
		mcop, pollError = ctrl.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
		if pollError != nil {
			klog.Errorf("MachineConfiguration/cluster has not been created yet")
			return false, nil
		}

		// Ensure status.ObservedGeneration matches the last generation of MachineConfiguration
		if mcop.Generation != mcop.Status.ObservedGeneration {
			klog.Errorf("MachineConfiguration.Status is not up to date.")
			pollError = fmt.Errorf("MachineConfiguration.Status is not up to date")
			return false, nil
		}
		return true, nil
	}); err != nil {
		klog.Errorf("MachineConfiguration was not ready: %v", pollError)
		ctrl.updateConditions(reason, fmt.Errorf("MachineConfiguration was not ready:  while enqueueing ControlPlaneMachineSet %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	machineManagerFound, machineResourceSelector, err := getMachineResourceSelectorFromMachineManagers(mcop.Status.ManagedBootImagesStatus.MachineManagers, opv1.MachineAPI, opv1.MachineSets)
	if err != nil {
		klog.Errorf("failed to create a machineset selector while enqueueing MAPI machineset %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to create a machineset selector while enqueueing ControlPlaneMachineSet %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}
	if !machineManagerFound {
		klog.V(4).Infof("No ControlPlaneMachineSet manager was found, so no ControlPlaneMachineSet will be enrolled.")
		// clear out MAPI boot image history
		for k := range ctrl.cpmsBootImageState {
			delete(ctrl.cpmsBootImageState, k)
		}

	}

	controlPlaneMachineSets, err := ctrl.cpmsLister.List(machineResourceSelector)
	if err != nil {
		klog.Errorf("failed to fetch ControlPlaneMachineSet list while enqueueing ControlPlaneMachineSet %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to fetch ControlPlaneMachineSet list while enqueueing ControlPlaneMachineSet %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	// If no machine resources were enrolled; exit the enqueue process without errors.
	if len(controlPlaneMachineSets) == 0 {
		klog.Infof("No ControlPlaneMachineSet were enrolled, so no MAPI machinesets will be enqueued.")
		// clear out ControlPlaneMachineSet boot image history
		for k := range ctrl.mapiBootImageState {
			delete(ctrl.mapiBootImageState, k)
		}
	}

	// Reset stats before initiating reconciliation loop
	ctrl.cpmsStats.inProgress = 0
	ctrl.cpmsStats.totalCount = len(controlPlaneMachineSets)
	ctrl.cpmsStats.erroredCount = 0

	// Signal start of reconciliation process, by setting progressing to true
	var syncErrors []error
	ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)

	for _, controlPlaneMachineSet := range controlPlaneMachineSets {
		err := ctrl.syncControlPlaneMachineSet(controlPlaneMachineSet)
		if err == nil {
			ctrl.cpmsStats.inProgress++
		} else {
			klog.Errorf("Error syncing ControlPlaneMachineSet %v", err)
			syncErrors = append(syncErrors, fmt.Errorf("error syncing ControlPlaneMachineSet %s: %v", controlPlaneMachineSet.Name, err))
			ctrl.cpmsStats.erroredCount++
		}
		// Update progressing conditions every step of the loop
		ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)
	}
	// Update/Clear degrade conditions based on errors from this loop
	ctrl.updateConditions(reason, kubeErrs.NewAggregate(syncErrors), opv1.MachineConfigurationBootImageUpdateDegraded)
}

// syncControlPlaneMachineSet will attempt to reconcile the provided ControlPlaneMachineSet
func (ctrl *Controller) syncControlPlaneMachineSet(controlPlaneMachineSet *machinev1.ControlPlaneMachineSet) error {

	startTime := time.Now()
	klog.V(4).Infof("Started syncing ControlPlaneMachineSet %q (%v)", controlPlaneMachineSet.Name, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing ControlPlaneMachineSet %q (%v)", controlPlaneMachineSet.Name, time.Since(startTime))
	}()

	// If the machineset has an owner reference, exit and report error. This means
	// that the machineset may be managed by another workflow and should not be reconciled.
	if len(controlPlaneMachineSet.GetOwnerReferences()) != 0 {
		klog.Infof("ControlPlaneMachineSet %s has OwnerReference: %v, skipping boot image update", controlPlaneMachineSet.GetOwnerReferences()[0].Kind+"/"+controlPlaneMachineSet.GetOwnerReferences()[0].Name, controlPlaneMachineSet.Name)
		return nil
	}

	if os, ok := controlPlaneMachineSet.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.Labels[OSLabelKey]; ok {
		if os == "Windows" {
			klog.Infof("ControlPlaneMachineSet %s has a windows os label, skipping boot image update", controlPlaneMachineSet.Name)
			return nil
		}
	}

	// ControlPlaneMachineSets do not normally have an arch annotation, so use the architecture of the node
	// running this pod, which will always be a control plane node.
	arch := archtranslater.CurrentRpmArch()

	// Fetch the infra object to determine the platform type
	infra, err := ctrl.infraLister.Get("cluster")
	if err != nil {
		return fmt.Errorf("failed to fetch infra object during ControlPlaneMachineSet sync: %w", err)
	}

	// Fetch the bootimage configmap & ensure it has been stamped by the operator. This is done by
	// the operator when a master node successfully updates to a new image. This is
	// to prevent machinesets from being updated before the operator itself has updated.
	// If it hasn't been updated, exit and wait for a resync.
	configMap, err := ctrl.mcoCmLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.BootImagesConfigMapName)
	if err != nil {
		return fmt.Errorf("failed to fetch coreos-bootimages config map duringControlPlaneMachineSet sync: %w", err)
	}
	versionHashFromCM, versionHashFound := configMap.Data[ctrlcommon.MCOVersionHashKey]
	if !versionHashFound {
		klog.Infof("failed to find mco version hash in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
		return nil
	}
	if versionHashFromCM != operatorversion.Hash {
		klog.Infof("mismatch between MCO hash version stored in configmap and current MCO version; sync will exit to wait for the MCO upgrade to complete")
		return nil
	}
	releaseVersionFromCM, releaseVersionFound := configMap.Data[ctrlcommon.OCPReleaseVersionKey]
	if !releaseVersionFound {
		klog.Infof("failed to find OCP release version in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
		return nil
	}
	if releaseVersionFromCM != operatorversion.ReleaseVersion {
		klog.Infof("mismatch between OCP release version stored in configmap and current MCO release version; sync will exit to wait for the MCO upgrade to complete")
		return nil
	}

	// Check if the this ControlPlaneMachineSet requires an update
	patchRequired, newControlPlaneMachineSet, err := checkControlPlaneMachineSet(infra, controlPlaneMachineSet, configMap, arch, ctrl.kubeClient)
	if err != nil {
		return fmt.Errorf("failed to reconcile ControlPlaneMachineSet %s, err: %w", controlPlaneMachineSet.Name, err)
	}

	// Patch the machineset if required
	if patchRequired {
		// First, check if we're hot looping
		if ctrl.checkControlPlaneMachineSetHotLoop(newControlPlaneMachineSet) {
			return fmt.Errorf("refusing to reconcile ControlPlaneMachineSet %s, hot loop detected. Please opt-out of boot image updates, adjust your machine provisioning workflow to prevent hot loops and opt back in to resume boot image updates", controlPlaneMachineSet.Name)
		}
		klog.Infof("Patching ControlPlaneMachineSet %s", controlPlaneMachineSet.Name)
		return ctrl.patchControlPlaneMachineSet(controlPlaneMachineSet, newControlPlaneMachineSet)
	}
	klog.Infof("No patching required for ControlPlaneMachineSet %s", controlPlaneMachineSet.Name)
	return nil
}

// Checks against a local store of boot image updates to detect hot looping
func (ctrl *Controller) checkControlPlaneMachineSetHotLoop(machineSet *machinev1.ControlPlaneMachineSet) bool {
	bis, ok := ctrl.cpmsBootImageState[machineSet.Name]
	if !ok {
		// If the machineset doesn't currently have a record, create a new one.
		ctrl.cpmsBootImageState[machineSet.Name] = BootImageState{
			value:        machineSet.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec.Value.Raw,
			hotLoopCount: 1,
		}
	} else {
		hotLoopCount := 1
		// If the controller is updating to a value that was previously updated to, increase the hot loop counter
		if bytes.Equal(bis.value, machineSet.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec.Value.Raw) {
			hotLoopCount = (bis.hotLoopCount) + 1
		}
		// Return an error and degrade if the hot loop counter is above threshold
		if hotLoopCount > HotLoopLimit {
			return true
		}
		ctrl.cpmsBootImageState[machineSet.Name] = BootImageState{
			value:        machineSet.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec.Value.Raw,
			hotLoopCount: hotLoopCount,
		}
	}
	return false
}

// This function patches the ControlPlaneMachineSet object using the machineClient
// Returns an error if marshsalling or patching fails.
func (ctrl *Controller) patchControlPlaneMachineSet(oldControlPlaneMachineSet, newControlPlaneMachineSet *machinev1.ControlPlaneMachineSet) error {
	oldControlPlaneMachineSetMarshal, err := json.Marshal(oldControlPlaneMachineSet)
	if err != nil {
		return fmt.Errorf("unable to marshal old ControlPlaneMachineSet: %w", err)
	}
	newControlPlaneMachineSetMarshal, err := json.Marshal(newControlPlaneMachineSet)
	if err != nil {
		return fmt.Errorf("unable to marshal new ControlPlaneMachineSet: %w", err)
	}
	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(oldControlPlaneMachineSetMarshal, newControlPlaneMachineSetMarshal, oldControlPlaneMachineSetMarshal)
	if err != nil {
		return fmt.Errorf("unable to create patch for new ControlPlaneMachineSet: %w", err)
	}
	_, err = ctrl.machineClient.MachineV1().ControlPlaneMachineSets(MachineAPINamespace).Patch(context.TODO(), oldControlPlaneMachineSet.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("unable to patch new ControlPlaneMachineSet: %w", err)
	}
	klog.Infof("Successfully patched ControlPlaneMachineSet %s", oldControlPlaneMachineSet.Name)
	return nil
}

// Returns architecture type for a given ControlPlaneMachineSet
func getArchFromControlPlaneMachineSet(controlPlaneMachineSet *machinev1.ControlPlaneMachineSet) (arch string, err error) {

	// Valid set of machineset/node architectures
	validArchSet := sets.New[string]("arm64", "s390x", "amd64", "ppc64le")
	// Check if the annotation enclosing arch label is present on this machineset
	archLabel, archLabelMatch := controlPlaneMachineSet.Annotations[MachineSetArchAnnotationKey]
	if archLabelMatch {
		// Grab arch value from the annotation and check if it is valid
		_, archLabelValue, archLabelValueFound := strings.Cut(archLabel, ArchLabelKey)
		if archLabelValueFound && validArchSet.Has(archLabelValue) {
			return archtranslater.RpmArch(archLabelValue), nil
		}
		return "", fmt.Errorf("invalid architecture value found in annotation: %s ", archLabel)
	}
	// If no arch annotation was found on the machineset, default to the control plane arch.
	// return the architecture of the node running this pod, which will always be a control plane node.
	klog.Infof("Defaulting to control plane architecture")
	return archtranslater.CurrentRpmArch(), nil
}

// This function calls the appropriate reconcile function based on the infra type
// On success, it will return a bool indicating if a patch is required, and an updated
// machineset object if any. It will return an error if any of the above steps fail.
func checkControlPlaneMachineSet(infra *osconfigv1.Infrastructure, machineSet *machinev1.ControlPlaneMachineSet, configMap *corev1.ConfigMap, arch string, secretClient clientset.Interface) (bool, *machinev1.ControlPlaneMachineSet, error) {
	switch infra.Status.PlatformStatus.Type {
	case osconfigv1.AWSPlatformType:
		return reconcileAWSCPMS(machineSet, configMap, arch, secretClient)
	case osconfigv1.AzurePlatformType:
		return reconcileAzureCPMS(machineSet, configMap, arch)
	case osconfigv1.GCPPlatformType:
		return reconcileGCPCPMS(machineSet, configMap, arch, secretClient)
	case osconfigv1.VSpherePlatformType:
		return reconcileVSphereCPMS(machineSet, configMap, arch)
	default:
		klog.Infof("Skipping control plane machineset %s, unsupported platform %s", machineSet.Name, infra.Status.PlatformStatus.Type)
		return false, nil, nil
	}
}

// This function unmarshals the controlplanemachineset's provider spec into
// a ProviderSpec object. Returns an error if providerSpec field is nil,
// or the unmarshal fails
func unmarshalProviderSpecCPMS(ms *machinev1.ControlPlaneMachineSet, providerSpec interface{}) error {
	if ms == nil {
		return fmt.Errorf("ControlPlaneMachineSet object was nil")
	}
	if ms.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec.Value == nil {
		return fmt.Errorf("providerSpec field was empty")
	}
	if err := yaml.Unmarshal(ms.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec.Value.Raw, &providerSpec); err != nil {
		return fmt.Errorf("unmarshal into providerSpec failed %w", err)
	}
	return nil
}

// This function marshals the ProviderSpec object into a ControlPlaneMachineSet object.
// Returns an error if ProviderSpec or ControlPlaneMachineSet is nil, or if the marshal fails
func marshalProviderSpecCPMS(ms *machinev1.ControlPlaneMachineSet, providerSpec interface{}) error {
	if ms == nil {
		return fmt.Errorf("ControlPlaneMachineSet object was nil")
	}
	if providerSpec == nil {
		return fmt.Errorf("ProviderSpec object was nil")
	}
	rawBytes, err := json.Marshal(providerSpec)
	if err != nil {
		return fmt.Errorf("marshal into machineset failed: %w", err)
	}
	ms.Spec.Template.OpenShiftMachineV1Beta1Machine.Spec.ProviderSpec.Value = &kruntime.RawExtension{Raw: rawBytes}
	return nil
}

func reconcileGCPCPMS(machineSet *machinev1.ControlPlaneMachineSet, configMap *corev1.ConfigMap, arch string, secretClient clientset.Interface) (patchRequired bool, newMachineSet *machinev1.ControlPlaneMachineSet, err error) {
	klog.Infof("Reconciling ControlPlaneMachineSet %s on GCP, with arch %s", machineSet.Name, arch)

	// First, unmarshal the GCP providerSpec
	providerSpec := new(machinev1beta1.GCPMachineProviderSpec)
	if err := unmarshalProviderSpecCPMS(machineSet, providerSpec); err != nil {
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
	if err := marshalProviderSpecCPMS(newMachineSet, newProviderSpec); err != nil {
		return false, nil, err
	}
	return patchRequired, newMachineSet, nil
}

func reconcileAWSCPMS(machineSet *machinev1.ControlPlaneMachineSet, configMap *corev1.ConfigMap, arch string, secretClient clientset.Interface) (patchRequired bool, newMachineSet *machinev1.ControlPlaneMachineSet, err error) {

	klog.Infof("Reconciling ControlPlaneMachineSet %s on AWS, with arch %s", machineSet.Name, arch)

	// First, unmarshal the AWS providerSpec
	providerSpec := new(machinev1beta1.AWSMachineProviderConfig)
	if err := unmarshalProviderSpecCPMS(machineSet, providerSpec); err != nil {
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
	if err := marshalProviderSpecCPMS(newMachineSet, newProviderSpec); err != nil {
		return false, nil, err
	}
	return patchRequired, newMachineSet, nil
}

func reconcileAzureCPMS(machineSet *machinev1.ControlPlaneMachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1.ControlPlaneMachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type Azure with %s arch", machineSet.Name, arch)
	return false, nil, nil
}
func reconcileVSphereCPMS(machineSet *machinev1.ControlPlaneMachineSet, _ *corev1.ConfigMap, arch string) (patchRequired bool, newMachineSet *machinev1.ControlPlaneMachineSet, err error) {
	klog.Infof("Skipping machineset %s, unsupported platform type VSphere with %s arch", machineSet.Name, arch)
	return false, nil, nil
}
