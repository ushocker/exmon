package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

const armBase = "https://management.azure.com"

// armCall makes one Azure Resource Manager call with the az CLI's token.
func armCall(method, path string, body, out any) error {
	token, err := azToken("https://management.azure.com/")
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	u := path
	if len(u) > 0 && u[0] == '/' {
		u = armBase + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		json.Unmarshal(b, &e)
		return fmt.Errorf("Azure: HTTP %d %s %s", resp.StatusCode, e.Error.Code, e.Error.Message)
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// eventGridClient is one device as Event Grid knows it.
type eventGridClient struct {
	Name       string `json:"name"`
	Properties struct {
		AuthenticationName string            `json:"authenticationName"`
		State              string            `json:"state"`
		Attributes         map[string]string `json:"attributes"`
	} `json:"properties"`
}

const eventGridAPI = "2025-02-15"

// listClients returns every client in the namespace.
func listClients(p Profile) ([]eventGridClient, error) {
	if err := p.needARM(); err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.EventGrid/namespaces/%s/clients?api-version=%s",
		url.PathEscape(p.Subscription), url.PathEscape(p.ResourceGroup), url.PathEscape(p.Namespace), eventGridAPI)
	var all []eventGridClient
	for path != "" {
		var page struct {
			Value    []eventGridClient `json:"value"`
			NextLink string            `json:"nextLink"`
		}
		if err := armCall(http.MethodGet, path, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Value...)
		path = page.NextLink
	}
	return all, nil
}

// deviceIDs returns the authentication names of the enabled devices in a ring.
func deviceIDs(p Profile, ring string) ([]string, error) {
	clients, err := listClients(p)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range clients {
		a := c.Properties.Attributes
		if a["role"] == "device" && a["ring"] == ring && c.Properties.State != "Disabled" {
			ids = append(ids, authName(c))
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no enabled devices in ring %q", ring)
	}
	return ids, nil
}

func authName(c eventGridClient) string {
	if c.Properties.AuthenticationName != "" {
		return c.Properties.AuthenticationName
	}
	return c.Name
}

// startRunbook starts the Automation runbook now (instead of waiting for its
// hourly schedule).
func startRunbook(p Profile) (string, error) {
	if p.Subscription == "" || p.ResourceGroup == "" || p.AutomationAccount == "" || p.Runbook == "" {
		return "", fmt.Errorf("the profile needs subscription, resourceGroup, automationAccount and runbook")
	}
	var b [16]byte
	rand.Read(b[:])
	job := hex.EncodeToString(b[:])
	path := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Automation/automationAccounts/%s/jobs/%s?api-version=2023-11-01",
		url.PathEscape(p.Subscription), url.PathEscape(p.ResourceGroup), url.PathEscape(p.AutomationAccount), job)
	err := armCall(http.MethodPut, path, map[string]any{"properties": map[string]any{"runbook": map[string]string{"name": p.Runbook}}}, nil)
	return job, err
}
