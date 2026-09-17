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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const defaultBaseURL = "https://api.vultr.com/v2"

// Client performs the Vultr API operations needed by Cluster Autoscaler.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	userAgent  string
}

// NewClient creates a Vultr API client.
func NewClient(client *http.Client) *Client {
	baseURL, err := url.Parse(defaultBaseURL)
	if err != nil {
		panic(err)
	}
	return &Client{
		httpClient: client,
		baseURL:    baseURL,
		userAgent:  "vultr-cluster-autoscaler",
	}
}

// SetBaseURL changes the API base URL. It is primarily useful for tests.
func (c *Client) SetBaseURL(baseURL string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	c.baseURL = u
	return c, nil
}

// SetUserAgent changes the User-Agent header sent with API requests.
func (c *Client) SetUserAgent(userAgent string) *Client {
	c.userAgent = userAgent
	return c
}

func (c *Client) newRequest(method, uri string, body any) (*http.Request, error) {
	resolvedURL, err := c.baseURL.Parse(uri)
	if err != nil {
		return nil, err
	}

	buf := new(bytes.Buffer)
	if body != nil {
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequest(method, resolvedURL.String(), buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (c *Client) do(ctx context.Context, req *http.Request, data any) error {
	res, err := c.httpClient.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < http.StatusOK || res.StatusCode > http.StatusNoContent {
		return fmt.Errorf("Vultr API returned %s: %s", res.Status, string(body))
	}
	if data == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, data)
}
