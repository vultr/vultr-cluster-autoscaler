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
	"strings"

	"github.com/vultr/govultr/v3"
	apiv1 "k8s.io/api/core/v1"
	"sigs.k8s.io/cluster-autoscaler/pkg/cloudprovider"
	"sigs.k8s.io/cluster-autoscaler/pkg/config"
	"sigs.k8s.io/cluster-autoscaler/pkg/simulator/framework"
)

const (
	vkeLabel    = "vke.vultr.com"
	nodeIDLabel = vkeLabel + "/node-id"
)

var errMissingNodeID = errors.New("missing node ID")

// NodeGroup implements cloudprovider.NodeGroup for a VKE node pool.
type NodeGroup struct {
	id        string
	clusterID string
	client    vultrClient
	nodePool  *govultr.NodePool
	minSize   int
	maxSize   int

	// pendingTargetSize is set only for scale-ups initiated by this provider.
	pendingTargetSize int
}

// MaxSize returns the maximum node pool size.
func (n *NodeGroup) MaxSize(context.Context) int { return n.maxSize }

// MinSize returns the minimum node pool size.
func (n *NodeGroup) MinSize(context.Context) int { return n.minSize }

// TargetSize returns the requested node pool size.
func (n *NodeGroup) TargetSize(context.Context) (int, error) {
	return n.nodePool.NodeQuantity, nil
}

// IncreaseSize increases the requested node pool size.
func (n *NodeGroup) IncreaseSize(ctx context.Context, delta int) error {
	if delta <= 0 {
		return fmt.Errorf("delta must be positive, have: %d", delta)
	}
	targetSize := n.nodePool.NodeQuantity + delta
	if targetSize > n.MaxSize(ctx) {
		return fmt.Errorf("size increase is too large. current: %d desired: %d max: %d", n.nodePool.NodeQuantity, targetSize, n.MaxSize(ctx))
	}

	updatedNodePool, _, err := n.client.UpdateNodePool(ctx, n.clusterID, n.id, &govultr.NodePoolReqUpdate{NodeQuantity: targetSize})
	if err != nil {
		return err
	}
	if updatedNodePool != nil && updatedNodePool.NodeQuantity != targetSize {
		return fmt.Errorf("couldn't increase size to %d (delta: %d). Current size is: %d", targetSize, delta, updatedNodePool.NodeQuantity)
	}

	n.nodePool.NodeQuantity = targetSize
	n.pendingTargetSize = targetSize
	return nil
}

// AtomicIncreaseSize is not supported.
func (n *NodeGroup) AtomicIncreaseSize(context.Context, int) error {
	return cloudprovider.ErrNotImplemented
}

// DeleteNodes deletes named instances from this node pool.
func (n *NodeGroup) DeleteNodes(ctx context.Context, nodes []*apiv1.Node) error {
	for _, node := range nodes {
		nodeID, err := nodeIDFromNode(node)
		if err != nil {
			return fmt.Errorf("cannot delete node %q on node pool %q: %w", node.Name, n.id, err)
		}
		if !n.hasNode(nodeID) {
			return fmt.Errorf("cannot delete node %q (%q): node does not belong to node pool %q", node.Name, nodeID, n.id)
		}
		if err := n.client.DeleteNodePoolInstance(ctx, n.clusterID, n.id, nodeID); err != nil {
			return fmt.Errorf("deleting node failed for cluster %q, node pool %q, node %q: %w", n.clusterID, n.id, nodeID, err)
		}
		n.nodePool.NodeQuantity--
		n.pendingTargetSize = 0
	}
	return nil
}

// ForceDeleteNodes is not supported.
func (n *NodeGroup) ForceDeleteNodes(context.Context, []*apiv1.Node) error {
	return cloudprovider.ErrNotImplemented
}

// DecreaseTargetSize reduces an unfulfilled scale-up request.
func (n *NodeGroup) DecreaseTargetSize(ctx context.Context, delta int) error {
	if delta >= 0 {
		return fmt.Errorf("delta must be negative, have: %d", delta)
	}
	targetSize := n.nodePool.NodeQuantity + delta
	if targetSize < n.MinSize(ctx) {
		return fmt.Errorf("size decrease is too small. current: %d desired: %d min: %d", n.nodePool.NodeQuantity, targetSize, n.MinSize(ctx))
	}
	if targetSize < len(n.nodePool.Nodes) {
		return fmt.Errorf("cannot decrease target size below existing nodes. current target: %d desired: %d existing nodes: %d", n.nodePool.NodeQuantity, targetSize, len(n.nodePool.Nodes))
	}

	updatedNodePool, _, err := n.client.UpdateNodePool(ctx, n.clusterID, n.id, &govultr.NodePoolReqUpdate{NodeQuantity: targetSize})
	if err != nil {
		return err
	}
	if updatedNodePool != nil && updatedNodePool.NodeQuantity != targetSize {
		return fmt.Errorf("couldn't decrease size to %d (delta: %d). Current size is: %d", targetSize, delta, updatedNodePool.NodeQuantity)
	}
	n.nodePool.NodeQuantity = targetSize
	n.pendingTargetSize = 0
	return nil
}

// Id returns the node pool ID.
func (n *NodeGroup) Id() string { return n.id }

// Debug returns a human-readable node pool description.
func (n *NodeGroup) Debug(ctx context.Context) string {
	return fmt.Sprintf("node group ID: %s (min:%d max:%d)", n.Id(), n.MinSize(ctx), n.MaxSize(ctx))
}

// Nodes returns the instances belonging to the node pool.
func (n *NodeGroup) Nodes(context.Context) ([]cloudprovider.Instance, error) {
	if n.nodePool == nil {
		return nil, errors.New("node pool instance is not created")
	}
	instances := make([]cloudprovider.Instance, 0, len(n.nodePool.Nodes))
	for _, node := range n.nodePool.Nodes {
		instances = append(instances, cloudprovider.Instance{Id: toProviderID(node.ID), Status: vultrNodeStatus(node.Status)})
	}
	return instances, nil
}

// TemplateNodeInfo is not supported. VKE node pools must have a registered node for scale-up simulation.
func (n *NodeGroup) TemplateNodeInfo(context.Context) (*framework.NodeInfo, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// Exist reports whether this node group maps to a VKE node pool.
func (n *NodeGroup) Exist(context.Context) bool { return n.nodePool != nil }

// Create is not supported.
func (n *NodeGroup) Create(context.Context) (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// Delete is not supported.
func (n *NodeGroup) Delete(context.Context) error { return cloudprovider.ErrNotImplemented }

// Autoprovisioned reports that VKE node pools are not created by Cluster Autoscaler.
func (n *NodeGroup) Autoprovisioned(context.Context) bool { return false }

// GetOptions uses the global node group autoscaling options.
func (n *NodeGroup) GetOptions(context.Context, config.NodeGroupAutoscalingOptions) (*config.NodeGroupAutoscalingOptions, error) {
	return nil, cloudprovider.ErrNotImplemented
}

func (n *NodeGroup) hasNode(nodeID string) bool {
	for _, node := range n.nodePool.Nodes {
		if node.ID == nodeID {
			return true
		}
	}
	return false
}

func nodeIDFromNode(node *apiv1.Node) (string, error) {
	if nodeID := node.Labels[nodeIDLabel]; nodeID != "" {
		return nodeID, nil
	}
	if node.Spec.ProviderID == "" {
		return "", fmt.Errorf("%w: missing provider ID and node ID label %q", errMissingNodeID, nodeIDLabel)
	}
	return toNodeID(node.Spec.ProviderID)
}

func vultrNodeStatus(status string) *cloudprovider.InstanceStatus {
	switch strings.ToLower(status) {
	case "active", "upgrading":
		return &cloudprovider.InstanceStatus{State: cloudprovider.InstanceRunning}
	case "closed":
		return &cloudprovider.InstanceStatus{State: cloudprovider.InstanceDeleting}
	case "suspended":
		return &cloudprovider.InstanceStatus{
			State: cloudprovider.InstanceRunning,
			ErrorInfo: &cloudprovider.InstanceErrorInfo{
				ErrorClass:   cloudprovider.OtherErrorClass,
				ErrorCode:    "suspended",
				ErrorMessage: "Vultr node is suspended",
			},
		}
	default:
		return &cloudprovider.InstanceStatus{State: cloudprovider.InstanceCreating}
	}
}
