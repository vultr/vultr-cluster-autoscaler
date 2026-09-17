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

package govultr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListNodePools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "/v2/kubernetes/clusters/cluster/node-pools", req.URL.Path)
		assert.Equal(t, "25", req.URL.Query().Get("per_page"))
		assert.Equal(t, "test-agent", req.Header.Get("User-Agent"))
		_, _ = w.Write([]byte(`{"node_pools":[{"id":"pool","auto_scaler":true}],"meta":{"total":1}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.Client()).SetBaseURL(server.URL)
	require.NoError(t, err)
	client.SetUserAgent("test-agent")

	pools, meta, err := client.ListNodePools(context.Background(), "cluster", &ListOptions{PerPage: 25})
	require.NoError(t, err)
	require.Len(t, pools, 1)
	assert.Equal(t, "pool", pools[0].ID)
	assert.Equal(t, 1, meta.Total)
}

func TestAPIErrorIncludesStatusAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer server.Close()

	client, err := NewClient(server.Client()).SetBaseURL(server.URL)
	require.NoError(t, err)
	_, _, err = client.ListNodePools(context.Background(), "cluster", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403 Forbidden")
	assert.Contains(t, err.Error(), "denied")
}
