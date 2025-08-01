// Code generated by informer-gen. DO NOT EDIT.

package v1

import (
	context "context"
	time "time"

	apimachineconfigurationv1 "github.com/openshift/api/machineconfiguration/v1"
	versioned "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	internalinterfaces "github.com/openshift/client-go/machineconfiguration/informers/externalversions/internalinterfaces"
	machineconfigurationv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// MachineConfigPoolInformer provides access to a shared informer and lister for
// MachineConfigPools.
type MachineConfigPoolInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() machineconfigurationv1.MachineConfigPoolLister
}

type machineConfigPoolInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewMachineConfigPoolInformer constructs a new informer for MachineConfigPool type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewMachineConfigPoolInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredMachineConfigPoolInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredMachineConfigPoolInformer constructs a new informer for MachineConfigPool type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredMachineConfigPoolInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineconfigurationV1().MachineConfigPools().List(context.Background(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineconfigurationV1().MachineConfigPools().Watch(context.Background(), options)
			},
			ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineconfigurationV1().MachineConfigPools().List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineconfigurationV1().MachineConfigPools().Watch(ctx, options)
			},
		},
		&apimachineconfigurationv1.MachineConfigPool{},
		resyncPeriod,
		indexers,
	)
}

func (f *machineConfigPoolInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredMachineConfigPoolInformer(client, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *machineConfigPoolInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&apimachineconfigurationv1.MachineConfigPool{}, f.defaultInformer)
}

func (f *machineConfigPoolInformer) Lister() machineconfigurationv1.MachineConfigPoolLister {
	return machineconfigurationv1.NewMachineConfigPoolLister(f.Informer().GetIndexer())
}
