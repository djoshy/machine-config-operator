package certrotationcontroller

import (
	"context"
	"encoding/json"
	"net/url"

	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	machineclientset "github.com/openshift/client-go/machine/clientset/versioned"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

func isUserDataSecret(secret corev1.Secret) bool {
	_, hasDerivedFromConfigMapLabel := secret.Labels[ctrlcommon.MachineConfigServerCAManagedByConfigMapKey]
	if hasDerivedFromConfigMapLabel {
		return false
	}
	// These secrets don't really have a label or not, so the determining factor is if they:
	// 1. have a userData field
	// 2. is an ignition config
	userData, exists := secret.Data[ctrlcommon.UserDataKey]
	if !exists {
		return false
	}
	// userData is an ignition config. To save the effort of multiple-version parsing, just parse it as a json
	var userDataIgn interface{}
	if err := json.Unmarshal(userData, &userDataIgn); err != nil {
		klog.Errorf("failed to unmarshal decoded user-data to json (secret %s): %v, skipping secret", secret.Name, err)
		return false
	}

	_, isIgn, err := unstructured.NestedMap(userDataIgn.(map[string]interface{}), ctrlcommon.IgnFieldIgnition)
	if !isIgn || err != nil {
		// Didn't find ignition in user-data, warn but continue
		klog.Infof("Unable to find ignition in user-data, skipping secret %s\n", secret.Name)
		return false
	}
	return true
}

func hasFunctionalMachineAPI(machineClient machineclientset.Interface) bool {
	machinesets, err := machineClient.MachineV1beta1().MachineSets(ctrlcommon.MachineAPINamespace).List(context.Background(), metav1.ListOptions{})
	// If we can't list machinesets, we consider the Machine API non-functional
	if err != nil {
		klog.Errorf("Error listing machines in namespace %s: %v", ctrlcommon.MachineAPINamespace, err)
		return false
	}
	// If there are any machinesets, we consider the Machine API functional
	return len(machinesets.Items) != 0
}

func hasFunctionalClusterAPI() bool {
	return false
}

func getHostnames(configClient configclientset.Interface) []string {
	cfg, err := configClient.ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		klog.Errorf("unable to get cluster infrastructure resource: %w", err)
		return nil
	}

	if cfg.Status.APIServerInternalURL == "" {
		klog.Errorf("no APIServerInternalURL found in cluster infrastructure resource")
		return nil
	}
	apiserverIntURL, err := url.Parse(cfg.Status.APIServerInternalURL)
	if err != nil {
		klog.Errorf("failed to parse %s: %v", apiserverIntURL, err)
		return nil
	}

	serverIPs := getServerIPsFromInfra(cfg)

	return append([]string{apiserverIntURL.Hostname()}, serverIPs...)
}
