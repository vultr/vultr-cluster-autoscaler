/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vultr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/informers"
	"k8s.io/klog/v2"
	"sigs.k8s.io/cluster-autoscaler/pkg/cloudprovider"
	"sigs.k8s.io/cluster-autoscaler/pkg/cloudprovider/builder"
	coreoptions "sigs.k8s.io/cluster-autoscaler/pkg/core/options"
	autoscalererrors "sigs.k8s.io/cluster-autoscaler/pkg/utils/errors"
	"sigs.k8s.io/cluster-autoscaler/pkg/utils/gpu"
)

// ProviderName is the cloud provider name registered with Cluster Autoscaler.
const ProviderName = "vultr"

const vultrProviderIDPrefix = "vultr://"

func init() {
	builder.RegisterCloudProvider(ProviderName, func(opts *coreoptions.AutoscalerOptions, _ cloudprovider.NodeGroupDiscoveryOptions, rl *cloudprovider.ResourceLimiter, _ informers.SharedInformerFactory) cloudprovider.CloudProvider {
		return Build(opts, rl)
	})
	builder.SetDefaultCloudProvider(ProviderName)
}

var _ cloudprovider.CloudProvider = (*vultrCloudProvider)(nil)

type vultrCloudProvider struct {
	manager         *manager
	resourceLimiter *cloudprovider.ResourceLimiter
}

func newVultrCloudProvider(manager *manager, resourceLimiter *cloudprovider.ResourceLimiter) *vultrCloudProvider {
	return &vultrCloudProvider{manager: manager, resourceLimiter: resourceLimiter}
}

// Name returns the provider name.
func (v *vultrCloudProvider) Name() string { return ProviderName }

// NodeGroups returns all autoscaling-enabled VKE node pools.
func (v *vultrCloudProvider) NodeGroups(context.Context) []cloudprovider.NodeGroup {
	nodeGroups := make([]cloudprovider.NodeGroup, len(v.manager.nodeGroups))
	for i, nodeGroup := range v.manager.nodeGroups {
		nodeGroups[i] = nodeGroup
	}
	return nodeGroups
}

// NodeGroupForNode returns the node group containing node.
func (v *vultrCloudProvider) NodeGroupForNode(_ context.Context, node *apiv1.Node) (cloudprovider.NodeGroup, error) {
	nodeID, err := nodeIDFromNode(node)
	if err != nil {
		if errors.Is(err, errMissingNodeID) {
			return nil, nil
		}
		return nil, err
	}
	for _, group := range v.manager.nodeGroups {
		if group.hasNode(nodeID) {
			return group, nil
		}
	}
	return nil, nil
}

// HasInstance reports whether node belongs to a known VKE node pool.
func (v *vultrCloudProvider) HasInstance(ctx context.Context, node *apiv1.Node) (bool, error) {
	nodeID, err := nodeIDFromNode(node)
	if err != nil {
		if errors.Is(err, errMissingNodeID) {
			return false, nil
		}
		return false, err
	}

	for _, group := range v.manager.nodeGroups {
		if group.hasNode(nodeID) {
			return true, nil
		}
	}

	// Nodes in pools not managed by this autoscaler still exist in Vultr.
	nodePools, _, _, err := v.manager.client.ListNodePools(ctx, v.manager.clusterID, nil)
	if err != nil {
		return true, err
	}
	for _, nodePool := range nodePools {
		for _, poolNode := range nodePool.Nodes {
			if poolNode.ID == nodeID {
				return true, nil
			}
		}
	}

	return false, nil
}

// Pricing is not supported by VKE.
func (v *vultrCloudProvider) Pricing(context.Context) (cloudprovider.PricingModel, autoscalererrors.AutoscalerError) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetAvailableMachineTypes is not supported because node pools are managed through the Vultr API.
func (v *vultrCloudProvider) GetAvailableMachineTypes(context.Context) ([]string, error) {
	return []string{}, nil
}

// NewNodeGroup is not supported because Cluster Autoscaler does not create VKE node pools.
func (v *vultrCloudProvider) NewNodeGroup(context.Context, string, map[string]string, map[string]string, []apiv1.Taint, map[string]resource.Quantity) (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetResourceLimiter returns the configured cluster resource limits.
func (v *vultrCloudProvider) GetResourceLimiter(context.Context) (*cloudprovider.ResourceLimiter, error) {
	return v.resourceLimiter, nil
}

// GPULabel returns no label because GPU discovery is not implemented.
func (v *vultrCloudProvider) GPULabel(context.Context) string { return "" }

// GetAvailableGPUTypes returns no GPU types because GPU discovery is not implemented.
func (v *vultrCloudProvider) GetAvailableGPUTypes(context.Context) map[string]struct{} { return nil }

// GetNodeGpuConfig returns GPU information recognized by the shared core.
func (v *vultrCloudProvider) GetNodeGpuConfig(ctx context.Context, node *apiv1.Node) *cloudprovider.GpuConfig {
	return gpu.GetNodeGPUFromCloudProvider(ctx, v, node)
}

// Cleanup releases provider resources.
func (v *vultrCloudProvider) Cleanup(context.Context) error { return nil }

// Refresh updates the node pool cache from the Vultr API.
func (v *vultrCloudProvider) Refresh(ctx context.Context) error {
	klog.V(4).Info("Refreshing node group cache")
	return v.manager.refresh(ctx)
}

func toProviderID(nodeID string) string { return vultrProviderIDPrefix + nodeID }

func toNodeID(providerID string) (string, error) {
	if !strings.HasPrefix(providerID, vultrProviderIDPrefix) {
		return "", fmt.Errorf("provider ID %q does not use expected prefix %q", providerID, vultrProviderIDPrefix)
	}
	nodeID := strings.TrimPrefix(providerID, vultrProviderIDPrefix)
	if nodeID == "" {
		return "", fmt.Errorf("provider ID %q does not contain a node ID", providerID)
	}
	return nodeID, nil
}

// Build constructs the Vultr cloud provider from Cluster Autoscaler options.
func Build(opts *coreoptions.AutoscalerOptions, resourceLimiter *cloudprovider.ResourceLimiter) cloudprovider.CloudProvider {
	if opts.CloudConfig == "" {
		klog.Fatal("No config file provided; specify one with --cloud-config")
	}

	configFile, err := os.Open(opts.CloudConfig)
	if err != nil {
		klog.Fatalf("Could not open cloud provider configuration file %q: %v", opts.CloudConfig, err)
	}
	defer configFile.Close()

	manager, err := newManager(configFile)
	if err != nil {
		klog.Fatalf("Failed to create Vultr manager: %v", err)
	}
	return newVultrCloudProvider(manager, resourceLimiter)
}
