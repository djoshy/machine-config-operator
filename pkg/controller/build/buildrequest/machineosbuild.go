package buildrequest

import (
	"context"
	//nolint:gosec
	"crypto/md5"
	"fmt"

	"github.com/distribution/reference"
	"github.com/ghodss/yaml"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// This is the same salt / pattern from pkg/controller/render/hash.go
var (
	// salt is 80 random bytes.
	// The salt was generated by `od -vAn -N80 -tu1 < /dev/urandom`. Do not change it.
	salt = []byte{
		16, 124, 206, 228, 139, 56, 175, 175, 79, 229, 134, 118, 157, 154, 211, 110,
		25, 93, 47, 253, 172, 106, 37, 7, 174, 13, 160, 185, 110, 17, 87, 52,
		219, 131, 12, 206, 218, 141, 116, 135, 188, 181, 192, 151, 233, 62, 126, 165,
		64, 83, 179, 119, 15, 168, 208, 197, 146, 107, 58, 227, 133, 188, 238, 26,
		33, 26, 235, 202, 32, 173, 31, 234, 41, 144, 148, 79, 6, 206, 23, 22,
	}
)

// Holds the objects that are used to construct a MachineOSBuild with a hashed
// name.
type MachineOSBuildOpts struct {
	MachineOSConfig   *mcfgv1alpha1.MachineOSConfig
	MachineConfigPool *mcfgv1.MachineConfigPool
	OSImageURLConfig  *ctrlcommon.OSImageURLConfig
}

// Validates that the required options are provided.
func (m *MachineOSBuildOpts) validateForHash() error {
	if m.MachineOSConfig == nil {
		return fmt.Errorf("missing required MachineOSConfig")
	}

	if m.MachineConfigPool == nil {
		return fmt.Errorf("missing required MachineConfigPool")
	}

	if m.MachineConfigPool.Name != m.MachineOSConfig.Spec.MachineConfigPool.Name {
		return fmt.Errorf("name mismatch, MachineConfigPool has %q, MachineOSConfig has %q", m.MachineConfigPool.Name, m.MachineOSConfig.Spec.MachineConfigPool.Name)
	}

	if m.OSImageURLConfig == nil {
		return fmt.Errorf("misssing OSImageURLConfig")
	}

	return nil
}

// Makes a deep-copy of the MachineOSConfig and clears the data from it that
// can come from either the MachineOSConfig or from the OSImageURLConfig
// object. We do this to stabilize the hashing of the name so that whether the
// value comes from the MachineOSConfig or the OSImageURLConfig, the hash will
// be same, provided that the value is the same.
func (m *MachineOSBuildOpts) getMachineOSConfigForHashing() *mcfgv1alpha1.MachineOSConfig {
	moscCopy := m.MachineOSConfig.DeepCopy()
	moscCopy.Spec.BuildInputs.BaseOSImagePullspec = ""
	moscCopy.Spec.BuildInputs.BaseOSExtensionsImagePullspec = ""
	moscCopy.Spec.BuildInputs.ReleaseVersion = ""
	return moscCopy
}

// Creates a list of objects that are consumed by the SHA256 hash.
func (m *MachineOSBuildOpts) objectsForHash() []interface{} {
	o := BuildRequestOpts{
		MachineOSConfig:  m.MachineOSConfig,
		OSImageURLConfig: m.OSImageURLConfig,
	}

	// The objects considered for hashing described inline:
	out := []interface{}{
		// The configuration of the MachineConfigPool object. This includes the
		// name of the rendered MachineConfig as well as the reference of all of
		// the individual MachineConfigs that went into that rendered
		// MachineConfig.
		m.MachineConfigPool.Spec.Configuration,
		// The deep-copy of the MachineOSConfig with the multisource data fields
		// removed for stability.
		m.getMachineOSConfigForHashing().Spec,
		// The complete OSImageURLConfig object.
		m.OSImageURLConfig,
		// The OS image extensions pullspec from either the MachineOSConfig or the
		// OSImageURLConfig.
		o.getExtensionsImagePullspec(),
		// The base OS image pullspec from either the MachineOSConfig or the
		// OSImageURLConfig.
		o.getBaseOSImagePullspec(),
		// The release version from either the MachineOSConfig or the
		// OSImageURLConfig.
		o.getReleaseVersion(),
	}

	return out
}

// Gets the hashed name including the MachineOSConfig name. This is in the
// format of "<mosc name>-<md5 hash>"
func (m *MachineOSBuildOpts) getHashedNameWithConfig() (string, error) {
	hash, err := m.getHashedName()
	if err != nil {
		return "", fmt.Errorf("could not get hashed name: %w", err)
	}

	return fmt.Sprintf("%s-%s", m.MachineOSConfig.Name, hash), nil
}

// Returns solely the hash of all of the provided objects.
func (m *MachineOSBuildOpts) getHashedName() (string, error) {
	if err := m.validateForHash(); err != nil {
		return "", fmt.Errorf("could not validate for hash: %w", err)
	}

	//nolint:gosec
	hasher := md5.New()
	if _, err := hasher.Write(salt); err != nil {
		return "", fmt.Errorf("error writing salt: %w", err)
	}

	for _, obj := range m.objectsForHash() {
		// Produce the hash by getting a YAML representation of each object that is
		// considered and writing the YAML bytes to the Write interface for the
		// hashing library.
		data, err := yaml.Marshal(obj)

		if err != nil {
			return "", fmt.Errorf("could not marshal object to YAML: %w", err)
		}

		if _, err := hasher.Write(data); err != nil {
			return "", fmt.Errorf("error writing object to hash: %w", err)
		}
	}

	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

// Constructs the MachineOSBuildOpts by retrieving the OSImageURLConfig from
// the API server.
func NewMachineOSBuildOpts(ctx context.Context, kubeclient clientset.Interface, mosc *mcfgv1alpha1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) (MachineOSBuildOpts, error) {
	// TODO: Consider an implementation that uses listers instead of API clients
	// just to cut down on API server traffic.
	osImageURLs, err := ctrlcommon.GetOSImageURLConfig(ctx, kubeclient)
	if err != nil {
		return MachineOSBuildOpts{}, fmt.Errorf("could not get OSImageURLConfig: %w", err)
	}

	return MachineOSBuildOpts{
		MachineOSConfig:   mosc,
		MachineConfigPool: mcp,
		OSImageURLConfig:  osImageURLs,
	}, nil
}

// Constructs a new MachineOSBuild object or panics trying. Useful for testing
// scenarios.
func NewMachineOSBuildOrDie(opts MachineOSBuildOpts) *mcfgv1alpha1.MachineOSBuild {
	mosb, err := NewMachineOSBuild(opts)

	if err != nil {
		panic(err)
	}

	return mosb
}

// Retrieves the MachineOSBuildOpts from the API and constructs a new
// MachineOSBuild object or panics trying. Useful for testing scenarios.
func NewMachineOSBuildFromAPIOrDie(ctx context.Context, kubeclient clientset.Interface, mosc *mcfgv1alpha1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) *mcfgv1alpha1.MachineOSBuild {
	mosb, err := NewMachineOSBuildFromAPI(ctx, kubeclient, mosc, mcp)

	if err != nil {
		panic(err)
	}

	return mosb
}

// Retrieves the MachineOSBuildOpts from the API and constructs a new
// MachineOSBuild object.
func NewMachineOSBuildFromAPI(ctx context.Context, kubeclient clientset.Interface, mosc *mcfgv1alpha1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) (*mcfgv1alpha1.MachineOSBuild, error) {
	opts, err := NewMachineOSBuildOpts(ctx, kubeclient, mosc, mcp)

	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSBuildOpts: %w", err)
	}

	return NewMachineOSBuild(opts)
}

// Constructs a new MachineOSBuild object with all of the labels, the tagged
// image pushpsec, and a hashed name.
func NewMachineOSBuild(opts MachineOSBuildOpts) (*mcfgv1alpha1.MachineOSBuild, error) {
	mosbName, err := opts.getHashedNameWithConfig()
	if err != nil {
		return nil, fmt.Errorf("could not get hashed name for MachineOSBuild: %w", err)
	}

	now := metav1.Now()

	namedRef, err := reference.ParseNamed(opts.MachineOSConfig.Spec.BuildInputs.RenderedImagePushspec)
	if err != nil {
		return nil, err
	}

	taggedRef, err := reference.WithTag(namedRef, mosbName)
	if err != nil {
		return nil, err
	}

	mosb := &mcfgv1alpha1.MachineOSBuild{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MachineOSBuild",
			APIVersion: "machineconfiguration.openshift.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   mosbName,
			Labels: utils.GetMachineOSBuildLabels(opts.MachineOSConfig, opts.MachineConfigPool),
		},
		Spec: mcfgv1alpha1.MachineOSBuildSpec{
			RenderedImagePushspec: taggedRef.String(),
			Version:               1,
			ConfigGeneration:      1,
			DesiredConfig: mcfgv1alpha1.RenderedMachineConfigReference{
				Name: opts.MachineConfigPool.Spec.Configuration.Name,
			},
			MachineOSConfig: mcfgv1alpha1.MachineOSConfigReference{
				Name: opts.MachineOSConfig.Name,
			},
		},
		Status: mcfgv1alpha1.MachineOSBuildStatus{
			BuildStart: &now,
		},
	}

	return mosb, nil
}
