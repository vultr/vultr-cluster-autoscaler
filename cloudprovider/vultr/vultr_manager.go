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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/vultr/govultr/v3"
	"golang.org/x/oauth2"
	"k8s.io/klog/v2"
)

type vultrClient interface {
	ListNodePools(ctx context.Context, vkeID string, options *govultr.ListOptions) ([]govultr.NodePool, *govultr.Meta, *http.Response, error)
	UpdateNodePool(ctx context.Context, vkeID, nodePoolID string, updateReq *govultr.NodePoolReqUpdate) (*govultr.NodePool, *http.Response, error)
	DeleteNodePoolInstance(ctx context.Context, vkeID, nodePoolID, nodeID string) error
}

type manager struct {
	clusterID  string
	client     vultrClient
	nodeGroups []*NodeGroup
}

// Config is the Vultr cloud provider configuration.
type Config struct {
	ClusterID string `json:"cluster_id"`
	Token     string `json:"token"`
}

func newManager(config io.Reader) (*manager, error) {
	cfg := &Config{}
	if config != nil {
		if err := json.NewDecoder(config).Decode(cfg); err != nil {
			return nil, err
		}
	}
	if cfg.Token == "" {
		return nil, errors.New("empty token was supplied")
	}
	if cfg.ClusterID == "" {
		return nil, errors.New("empty cluster ID was supplied")
	}

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token})
	httpClient := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &oauth2.Transport{
			Source: tokenSource,
		},
	}
	client := govultr.NewClient(httpClient)
	client.SetUserAgent("vultr-cluster-autoscaler")
	return &manager{
		client:     client.Kubernetes,
		nodeGroups: make([]*NodeGroup, 0),
		clusterID:  cfg.ClusterID,
	}, nil
}

func (m *manager) refresh(ctx context.Context) error {
	nodePools, _, _, err := m.client.ListNodePools(ctx, m.clusterID, nil)
	if err != nil {
		return err
	}

	pendingTargets := make(map[string]int, len(m.nodeGroups))
	for _, nodeGroup := range m.nodeGroups {
		if nodeGroup.pendingTargetSize > 0 {
			pendingTargets[nodeGroup.id] = nodeGroup.pendingTargetSize
		}
	}

	groups := make([]*NodeGroup, 0, len(nodePools))
	for _, nodePool := range nodePools {
		if !nodePool.AutoScaler {
			continue
		}
		klog.V(3).Infof("adding node pool %q with min nodes %d and max nodes %d", nodePool.Label, nodePool.MinNodes, nodePool.MaxNodes)

		pendingTargetSize := 0
		if pendingTarget, ok := pendingTargets[nodePool.ID]; ok && pendingTarget > nodePool.NodeQuantity && len(nodePool.Nodes) < pendingTarget {
			klog.V(4).Infof("preserving in-flight target size for node pool %q: Vultr target %d, pending target %d, existing nodes %d", nodePool.ID, nodePool.NodeQuantity, pendingTarget, len(nodePool.Nodes))
			nodePool.NodeQuantity = pendingTarget
			pendingTargetSize = pendingTarget
		}

		np := nodePool
		groups = append(groups, &NodeGroup{
			id:                nodePool.ID,
			clusterID:         m.clusterID,
			client:            m.client,
			nodePool:          &np,
			minSize:           nodePool.MinNodes,
			maxSize:           nodePool.MaxNodes,
			pendingTargetSize: pendingTargetSize,
		})
	}
	m.nodeGroups = groups
	return nil
}
