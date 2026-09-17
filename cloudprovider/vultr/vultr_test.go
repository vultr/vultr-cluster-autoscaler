/*
Copyright 2026 The Kubernetes Authors.

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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/vultr/vultr-cluster-autoscaler/cloudprovider/vultr/govultr"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-autoscaler/pkg/cloudprovider"
	cloudproviderbuilder "sigs.k8s.io/cluster-autoscaler/pkg/cloudprovider/builder"
)

type mockClient struct{ mock.Mock }

func TestProviderRegistration(t *testing.T) {
	assert.Equal(t, ProviderName, cloudproviderbuilder.DefaultCloudProvider())
	assert.Equal(t, []string{ProviderName}, cloudproviderbuilder.AvailableCloudProviders())
}

func (m *mockClient) ListNodePools(ctx context.Context, clusterID string, options *govultr.ListOptions) ([]govultr.NodePool, *govultr.Meta, error) {
	args := m.Called(ctx, clusterID, options)
	return args.Get(0).([]govultr.NodePool), args.Get(1).(*govultr.Meta), args.Error(2)
}

func (m *mockClient) UpdateNodePool(ctx context.Context, clusterID, nodePoolID string, req *govultr.NodePoolReqUpdate) (*govultr.NodePool, error) {
	args := m.Called(ctx, clusterID, nodePoolID, req)
	var nodePool *govultr.NodePool
	if args.Get(0) != nil {
		nodePool = args.Get(0).(*govultr.NodePool)
	}
	return nodePool, args.Error(1)
}

func (m *mockClient) DeleteNodePoolInstance(ctx context.Context, clusterID, nodePoolID, nodeID string) error {
	return m.Called(ctx, clusterID, nodePoolID, nodeID).Error(0)
}

func TestNewManagerValidatesConfig(t *testing.T) {
	manager, err := newManager(strings.NewReader(`{"token":"token","cluster_id":"cluster"}`))
	require.NoError(t, err)
	assert.Equal(t, "cluster", manager.clusterID)

	_, err = newManager(strings.NewReader(`{"cluster_id":"cluster"}`))
	assert.EqualError(t, err, "empty token was supplied")

	_, err = newManager(strings.NewReader(`{"token":"token"}`))
	assert.EqualError(t, err, "empty cluster ID was supplied")
}

func TestManagerRefreshFiltersPoolsAndPreservesScaleUpTarget(t *testing.T) {
	ctx := context.Background()
	client := new(mockClient)
	manager := &manager{
		clusterID: "cluster",
		client:    client,
		nodeGroups: []*NodeGroup{{
			id:       "autoscaled",
			nodePool: &govultr.NodePool{ID: "autoscaled", NodeQuantity: 3, Nodes: []govultr.Node{{ID: "node-1"}}},
		}},
	}
	client.On("ListNodePools", ctx, "cluster", (*govultr.ListOptions)(nil)).Return(
		[]govultr.NodePool{
			{ID: "autoscaled", AutoScaler: true, NodeQuantity: 1, MinNodes: 1, MaxNodes: 5, Nodes: []govultr.Node{{ID: "node-1"}}},
			{ID: "manual", AutoScaler: false, NodeQuantity: 2},
		},
		&govultr.Meta{},
		nil,
	).Once()

	require.NoError(t, manager.refresh(ctx))
	require.Len(t, manager.nodeGroups, 1)
	assert.Equal(t, 3, manager.nodeGroups[0].nodePool.NodeQuantity)
	client.AssertExpectations(t)
}

func TestCloudProviderFindsNodeGroup(t *testing.T) {
	nodeGroup := &NodeGroup{id: "pool", nodePool: &govultr.NodePool{Nodes: []govultr.Node{{ID: "node-1"}}}}
	provider := newVultrCloudProvider(&manager{nodeGroups: []*NodeGroup{nodeGroup}}, &cloudprovider.ResourceLimiter{})

	got, err := provider.NodeGroupForNode(context.Background(), &apiv1.Node{Spec: apiv1.NodeSpec{ProviderID: "vultr://node-1"}})
	require.NoError(t, err)
	assert.Same(t, nodeGroup, got)

	got, err = provider.NodeGroupForNode(context.Background(), &apiv1.Node{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestNodeGroupResizeAndDelete(t *testing.T) {
	ctx := context.Background()
	client := new(mockClient)
	nodeGroup := &NodeGroup{
		id:        "pool",
		clusterID: "cluster",
		client:    client,
		nodePool:  &govultr.NodePool{NodeQuantity: 2, Nodes: []govultr.Node{{ID: "node-1"}}},
		minSize:   1,
		maxSize:   4,
	}

	client.On("UpdateNodePool", ctx, "cluster", "pool", &govultr.NodePoolReqUpdate{NodeQuantity: 3}).Return(&govultr.NodePool{NodeQuantity: 3}, nil).Once()
	require.NoError(t, nodeGroup.IncreaseSize(ctx, 1))
	assert.Equal(t, 3, nodeGroup.nodePool.NodeQuantity)

	client.On("UpdateNodePool", ctx, "cluster", "pool", &govultr.NodePoolReqUpdate{NodeQuantity: 2}).Return(nil, nil).Once()
	require.NoError(t, nodeGroup.DecreaseTargetSize(ctx, -1))
	assert.Equal(t, 2, nodeGroup.nodePool.NodeQuantity)

	client.On("DeleteNodePoolInstance", ctx, "cluster", "pool", "node-1").Return(nil).Once()
	node := &apiv1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: map[string]string{nodeIDLabel: "node-1"}}}
	require.NoError(t, nodeGroup.DeleteNodes(ctx, []*apiv1.Node{node}))
	assert.Equal(t, 1, nodeGroup.nodePool.NodeQuantity)
	client.AssertExpectations(t)
}

func TestNodeGroupRejectsInvalidResizeAndForeignNode(t *testing.T) {
	nodeGroup := &NodeGroup{
		id:       "pool",
		nodePool: &govultr.NodePool{NodeQuantity: 2, Nodes: []govultr.Node{{ID: "node-1"}}},
		minSize:  1,
		maxSize:  2,
	}
	assert.Error(t, nodeGroup.IncreaseSize(context.Background(), 1))
	assert.Error(t, nodeGroup.DecreaseTargetSize(context.Background(), 0))
	assert.Error(t, nodeGroup.DeleteNodes(context.Background(), []*apiv1.Node{{Spec: apiv1.NodeSpec{ProviderID: "vultr://foreign"}}}))
}

func TestVultrNodeStatus(t *testing.T) {
	tests := map[string]cloudprovider.InstanceState{
		"active":    cloudprovider.InstanceRunning,
		"upgrading": cloudprovider.InstanceRunning,
		"pending":   cloudprovider.InstanceCreating,
		"closed":    cloudprovider.InstanceDeleting,
	}
	for status, want := range tests {
		assert.Equal(t, want, vultrNodeStatus(status).State)
	}
	assert.NotNil(t, vultrNodeStatus("suspended").ErrorInfo)
}

func TestNodeGroupReturnsClientErrors(t *testing.T) {
	ctx := context.Background()
	client := new(mockClient)
	nodeGroup := &NodeGroup{id: "pool", clusterID: "cluster", client: client, nodePool: &govultr.NodePool{NodeQuantity: 1}, minSize: 0, maxSize: 2}
	client.On("UpdateNodePool", ctx, "cluster", "pool", &govultr.NodePoolReqUpdate{NodeQuantity: 2}).Return(nil, errors.New("api error")).Once()
	assert.EqualError(t, nodeGroup.IncreaseSize(ctx, 1), "api error")
}
