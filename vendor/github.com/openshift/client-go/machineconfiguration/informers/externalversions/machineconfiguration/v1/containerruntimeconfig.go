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

// ContainerRuntimeConfigInformer provides access to a shared informer and lister for
// ContainerRuntimeConfigs.
type ContainerRuntimeConfigInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() machineconfigurationv1.ContainerRuntimeConfigLister
}

type containerRuntimeConfigInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewContainerRuntimeConfigInformer constructs a new informer for ContainerRuntimeConfig type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewContainerRuntimeConfigInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredContainerRuntimeConfigInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredContainerRuntimeConfigInformer constructs a new informer for ContainerRuntimeConfig type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredContainerRuntimeConfigInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineconfigurationV1().ContainerRuntimeConfigs().List(context.Background(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineconfigurationV1().ContainerRuntimeConfigs().Watch(context.Background(), options)
			},
			ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineconfigurationV1().ContainerRuntimeConfigs().List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.MachineconfigurationV1().ContainerRuntimeConfigs().Watch(ctx, options)
			},
		},
		&apimachineconfigurationv1.ContainerRuntimeConfig{},
		resyncPeriod,
		indexers,
	)
}

func (f *containerRuntimeConfigInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredContainerRuntimeConfigInformer(client, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *containerRuntimeConfigInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&apimachineconfigurationv1.ContainerRuntimeConfig{}, f.defaultInformer)
}

func (f *containerRuntimeConfigInformer) Lister() machineconfigurationv1.ContainerRuntimeConfigLister {
	return machineconfigurationv1.NewContainerRuntimeConfigLister(f.Informer().GetIndexer())
}
