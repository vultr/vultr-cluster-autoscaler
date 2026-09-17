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

package govultr

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/go-querystring/query"
)

const vkePath = "/v2/kubernetes/clusters"

// Nodepools describes the VKE node pool operations used by the provider.
type Nodepools interface {
	ListNodePools(ctx context.Context, vkeID string, options *ListOptions) ([]NodePool, *Meta, error)
	UpdateNodePool(ctx context.Context, vkeID, nodePoolID string, updateReq *NodePoolReqUpdate) (*NodePool, error)
	DeleteNodePoolInstance(ctx context.Context, vkeID, nodePoolID, nodeID string) error
}

// NodePool represents a VKE node pool.
type NodePool struct {
	ID           string `json:"id"`
	DateCreated  string `json:"date_created"`
	DateUpdated  string `json:"date_updated"`
	Label        string `json:"label"`
	Plan         string `json:"plan"`
	Status       string `json:"status"`
	NodeQuantity int    `json:"node_quantity"`
	Tag          string `json:"tag"`
	Nodes        []Node `json:"nodes"`
	AutoScaler   bool   `json:"auto_scaler"`
	MinNodes     int    `json:"min_nodes"`
	MaxNodes     int    `json:"max_nodes"`
}

// Node represents a node in a VKE node pool.
type Node struct {
	ID          string `json:"id"`
	DateCreated string `json:"date_created"`
	Label       string `json:"label"`
	Status      string `json:"status"`
}

// NodePoolReqUpdate contains mutable node pool fields.
type NodePoolReqUpdate struct {
	NodeQuantity int    `json:"node_quantity,omitempty"`
	Tag          string `json:"tag,omitempty"`
}

type vkeNodePoolsBase struct {
	NodePools []NodePool `json:"node_pools"`
	Meta      *Meta      `json:"meta"`
}

type vkeNodePoolBase struct {
	NodePool *NodePool `json:"node_pool"`
}

// ListNodePools returns all node pools for a VKE cluster.
func (c *Client) ListNodePools(ctx context.Context, vkeID string, options *ListOptions) ([]NodePool, *Meta, error) {
	req, err := c.newRequest(http.MethodGet, fmt.Sprintf("%s/%s/node-pools", vkePath, vkeID), nil)
	if err != nil {
		return nil, nil, err
	}

	values, err := query.Values(options)
	if err != nil {
		return nil, nil, err
	}
	req.URL.RawQuery = values.Encode()

	response := new(vkeNodePoolsBase)
	if err := c.do(ctx, req, response); err != nil {
		return nil, nil, err
	}
	return response.NodePools, response.Meta, nil
}

// UpdateNodePool updates a VKE node pool.
func (c *Client) UpdateNodePool(ctx context.Context, vkeID, nodePoolID string, updateReq *NodePoolReqUpdate) (*NodePool, error) {
	req, err := c.newRequest(http.MethodPatch, fmt.Sprintf("%s/%s/node-pools/%s", vkePath, vkeID, nodePoolID), updateReq)
	if err != nil {
		return nil, err
	}

	response := new(vkeNodePoolBase)
	if err := c.do(ctx, req, response); err != nil {
		return nil, err
	}
	return response.NodePool, nil
}

// DeleteNodePoolInstance deletes a specific node from a VKE node pool.
func (c *Client) DeleteNodePoolInstance(ctx context.Context, vkeID, nodePoolID, nodeID string) error {
	req, err := c.newRequest(http.MethodDelete, fmt.Sprintf("%s/%s/node-pools/%s/nodes/%s", vkePath, vkeID, nodePoolID, nodeID), nil)
	if err != nil {
		return err
	}
	return c.do(ctx, req, nil)
}
