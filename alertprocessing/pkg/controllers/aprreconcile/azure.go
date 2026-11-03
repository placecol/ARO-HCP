// Copyright 2026 Microsoft Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aprreconcile

import (
	"context"
	"errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertprocessingrules/armalertprocessingrules"
)

// APRClient is the subset of the Azure Alert Processing Rules API the reconcile loop needs.
// The regional resource group is bound by the implementation, so callers address resources
// by name only. It is an interface so tests can inject a fake.
type APRClient interface {
	// Get returns the named rule, or (nil, nil) if it does not exist.
	Get(ctx context.Context, name string) (*armalertprocessingrules.AlertProcessingRule, error)
	// CreateOrUpdate writes the named rule.
	CreateOrUpdate(ctx context.Context, name string, apr armalertprocessingrules.AlertProcessingRule) error
	// Delete removes the named rule. Deleting an absent rule is not an error.
	Delete(ctx context.Context, name string) error
}

// azureAPRClient is the production APRClient backed by the Azure SDK.
type azureAPRClient struct {
	client        *armalertprocessingrules.Client
	resourceGroup string
}

// NewAzureAPRClient wraps an armalertprocessingrules.Client bound to a resource group.
func NewAzureAPRClient(client *armalertprocessingrules.Client, resourceGroup string) APRClient {
	return &azureAPRClient{client: client, resourceGroup: resourceGroup}
}

func (c *azureAPRClient) Get(ctx context.Context, name string) (*armalertprocessingrules.AlertProcessingRule, error) {
	resp, err := c.client.GetByName(ctx, c.resourceGroup, name, nil)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &resp.AlertProcessingRule, nil
}

func (c *azureAPRClient) CreateOrUpdate(ctx context.Context, name string, apr armalertprocessingrules.AlertProcessingRule) error {
	_, err := c.client.CreateOrUpdate(ctx, c.resourceGroup, name, apr, nil)
	return err
}

func (c *azureAPRClient) Delete(ctx context.Context, name string) error {
	_, err := c.client.Delete(ctx, c.resourceGroup, name, nil)
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

func isNotFound(err error) bool {
	var respErr *azcore.ResponseError
	return errors.As(err, &respErr) && respErr != nil && respErr.StatusCode == 404
}

// existingActionGroupIDs returns the union of all action group IDs already present in the
// live resource's AddActionGroups actions. It ignores RemoveAllActionGroups actions (the
// controller never writes those). A nil resource yields an empty slice.
func existingActionGroupIDs(apr *armalertprocessingrules.AlertProcessingRule) []string {
	if apr == nil || apr.Properties == nil {
		return nil
	}
	var ids []string
	for _, action := range apr.Properties.Actions {
		add, ok := action.(*armalertprocessingrules.AddActionGroups)
		if !ok {
			continue
		}
		for _, id := range add.ActionGroupIDs {
			if id != nil {
				ids = append(ids, *id)
			}
		}
	}
	return ids
}
