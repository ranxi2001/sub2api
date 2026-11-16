package mihomo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func sourceTestManager(t *testing.T) *Manager {
	t.Helper()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(controller.Close)
	m := New(t.TempDir())
	t.Cleanup(m.Close)
	m.controllerURL = controller.URL
	m.state.Installed = true
	m.state.Running = true
	m.state.Supported = true
	m.saved.Secret = "test-only"
	require.NoError(t, os.WriteFile(filepath.Join(m.dir, "mihomo"), []byte("#!/bin/sh\nexit 0\n"), 0700))
	return m
}

func cachedSource(nodeName string) subscriptionCache {
	return subscriptionCache{Nodes: []map[string]any{{"name": nodeName, "type": "http", "server": "127.0.0.1", "port": 8080}}, Names: map[string]string{nodeName: "Node"}, UpdatedAt: time.Now().UTC()}
}

func waitSourceOperation(t *testing.T, m *Manager) Status {
	t.Helper()
	// Wait for the submitted operation itself. Repeated Status snapshots copy
	// large proxy lists and compete with config generation on busy CI runners.
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("source operation did not complete within 30 seconds")
	}
	status := m.Status()
	require.False(t, status.Busy)
	return status
}

func TestSourceRemovalPreservesCachedPeersDynamicAndSharedNodes(t *testing.T) {
	m := sourceTestManager(t)
	removed := "https://broken.invalid/private-token"
	remaining := "https://offline.invalid/?secret=kept"
	dynamic := "http://user:private-password@localhost:2000"
	m.saved.URLs = []string{removed, remaining}
	m.saved.DynamicProxies = []string{dynamic}
	m.saved.SubscriptionCache = map[string]subscriptionCache{subscriptionID(removed): cachedSource("shared"), subscriptionID(remaining): cachedSource("shared")}
	require.NoError(t, m.resolveSources(context.Background(), &m.saved, false))
	require.NoError(t, m.Submit("subscription_remove/"+subscriptionID(removed), nil, false))
	status := waitSourceOperation(t, m)
	require.Empty(t, status.Error)
	require.Equal(t, 1, status.Subscriptions)
	require.Equal(t, 1, status.DynamicProxies)
	require.Equal(t, 2, status.Nodes)
	require.Equal(t, []string{remaining}, m.saved.URLs)
	require.NotContains(t, m.saved.SubscriptionCache, subscriptionID(removed))
	b, err := os.ReadFile(filepath.Join(m.dir, "settings.json"))
	require.NoError(t, err)
	require.NotContains(t, string(b), "private-token")
	require.Contains(t, string(b), "private-password")
	public, err := json.Marshal(status)
	require.NoError(t, err)
	for _, secret := range []string{"offline.invalid", "kept", "private-password", "private-token"} {
		require.NotContains(t, string(public), secret)
	}
}

func TestSourceLastRemovalRejectsTrafficAndPersistsEmpty(t *testing.T) {
	m := sourceTestManager(t)
	address := "https://broken.invalid/sub"
	m.saved.URLs = []string{address}
	m.saved.SubscriptionCache = map[string]subscriptionCache{subscriptionID(address): cachedSource("old")}
	require.NoError(t, m.resolveSources(context.Background(), &m.saved, false))
	require.NoError(t, m.Submit("subscription_remove/"+subscriptionID(address), nil, false))
	status := waitSourceOperation(t, m)
	require.Empty(t, status.Error)
	require.Zero(t, status.Nodes)
	require.Zero(t, status.Subscriptions)
	b, err := m.config(m.saved)
	require.NoError(t, err)
	require.Contains(t, string(b), "REJECT")
	require.NotContains(t, string(b), "DIRECT")
	require.NotContains(t, string(b), "\"old\"")
	persisted, err := os.ReadFile(filepath.Join(m.dir, "settings.json"))
	require.NoError(t, err)
	var state saved
	require.NoError(t, json.Unmarshal(persisted, &state))
	require.Empty(t, state.URLs)
	require.Empty(t, state.Nodes)
}

func TestSourceIndividualRefreshFailureRetainsPriorConfiguration(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	m := sourceTestManager(t)
	address := server.URL + "/private-token"
	m.subscriptionProxyURL = server.URL
	m.saved.URLs = []string{address}
	m.saved.SubscriptionCache = map[string]subscriptionCache{subscriptionID(address): cachedSource("old")}
	require.NoError(t, m.resolveSources(context.Background(), &m.saved, false))
	old := m.saved.SubscriptionCache[subscriptionID(address)]
	require.NoError(t, m.Submit("subscription_refresh/"+subscriptionID(address), nil, false))
	status := waitSourceOperation(t, m)
	require.Contains(t, status.Error, "HTTP 403")
	require.NotContains(t, status.Error, "private-token")
	require.Equal(t, old, m.saved.SubscriptionCache[subscriptionID(address)])
	require.Equal(t, 1, status.Nodes)
	require.True(t, status.Running)
	require.Positive(t, requests.Load())
}

func TestSourceLegacyRemovalFetchesOnlyRemainingSource(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("proxies:\n - {name: retained, type: http, server: localhost, port: 9999}\n"))
	}))
	defer server.Close()
	m := sourceTestManager(t)
	removed := "https://broken.invalid/sub"
	m.subscriptionProxyURL = server.URL
	m.saved.URLs = []string{removed, server.URL}
	m.saved.Nodes = cachedSource("legacy").Nodes
	require.NoError(t, m.Submit("subscription_remove/"+subscriptionID(removed), nil, false))
	status := waitSourceOperation(t, m)
	require.Empty(t, status.Error)
	require.Equal(t, int32(1), requests.Load())
	require.Len(t, status.SubscriptionItems, 1)
	require.True(t, status.SubscriptionItems[0].Cached)
}

func TestSourceDisableEnableAndRenameDoNotFetch(t *testing.T) {
	m := sourceTestManager(t)
	address := "https://offline.invalid/token"
	m.saved.URLs = []string{address}
	m.saved.SubscriptionCache = map[string]subscriptionCache{subscriptionID(address): cachedSource("only")}
	require.NoError(t, m.resolveSources(context.Background(), &m.saved, false))
	for _, op := range []string{"subscription_disable", "subscription_enable"} {
		require.NoError(t, m.Submit(op+"/"+subscriptionID(address), nil, false))
		status := waitSourceOperation(t, m)
		require.Empty(t, status.Error)
		require.Equal(t, op == "subscription_enable", status.SubscriptionItems[0].Enabled)
		if op == "subscription_disable" {
			require.Zero(t, status.Nodes)
		} else {
			require.Equal(t, 1, status.Nodes)
		}
	}
	require.NoError(t, m.SubmitSourceManagement("subscription_rename/"+subscriptionID(address), nil, nil, false, "Primary provider"))
	status := waitSourceOperation(t, m)
	require.Empty(t, status.Error)
	require.Equal(t, "Primary provider", status.SubscriptionItems[0].Label)
	require.Error(t, m.Submit("subscription_remove/stale-id", nil, false))
	require.Len(t, m.saved.URLs, 1)
}

func TestSourceValidationDoesNotMutateSavedState(t *testing.T) {
	first := "https://first.invalid/sub"
	second := "https://second.invalid/sub"
	old := saved{URLs: []string{first, second}, DisabledSubscriptions: map[string]bool{subscriptionID(first): true}, SubscriptionCache: map[string]subscriptionCache{subscriptionID(first): cachedSource("first")}}
	_, err := prepareSourceChange(old, "subscription_update", subscriptionID(first), []string{second}, nil)
	require.Error(t, err)
	require.Equal(t, []string{first, second}, old.URLs)
	require.True(t, old.DisabledSubscriptions[subscriptionID(first)])
	require.Contains(t, old.SubscriptionCache, subscriptionID(first))
	_, err = prepareSourceChange(old, "subscription_update", subscriptionID(first), nil, nil)
	require.Error(t, err)
	_, err = prepareSourceChange(old, "dynamic_remove", "stale", nil, nil)
	require.Error(t, err)
	m := sourceTestManager(t)
	require.Error(t, m.Submit("subscription_remove", nil, false))
	require.Error(t, m.Submit("dynamic_clear/extra", nil, false))
	require.Error(t, m.SubmitSourceManagement("subscription_add", []string{first}, nil, false, strings.Repeat("x", 81)))
}

func TestSourceDynamicAppendAndRemovePreserveSubscriptions(t *testing.T) {
	m := sourceTestManager(t)
	address := "https://offline.invalid/sub"
	first := "http://user:one@localhost:2001"
	second := "http://user:two@localhost:2002"
	m.saved.URLs = []string{address}
	m.saved.SubscriptionCache = map[string]subscriptionCache{subscriptionID(address): cachedSource("airport")}
	m.saved.DynamicProxies = []string{first}
	require.NoError(t, m.resolveSources(context.Background(), &m.saved, false))
	require.NoError(t, m.SubmitWithDynamicProxies("dynamic_append", nil, []string{second}, false))
	status := waitSourceOperation(t, m)
	require.Empty(t, status.Error)
	require.Equal(t, 2, status.DynamicProxies)
	require.Equal(t, 1, status.Subscriptions)
	node, err := dynamicProxyNode(first)
	require.NoError(t, err)
	nodeName, ok := node["name"].(string)
	require.True(t, ok)
	require.NoError(t, m.Submit("dynamic_remove/"+nodeName, nil, false))
	status = waitSourceOperation(t, m)
	require.Empty(t, status.Error)
	require.Equal(t, 1, status.DynamicProxies)
	require.Equal(t, 1, status.Subscriptions)
	require.Equal(t, 2, status.Nodes)
	require.NoError(t, m.Submit("dynamic_clear", nil, false))
	status = waitSourceOperation(t, m)
	require.Empty(t, status.Error)
	require.Zero(t, status.DynamicProxies)
	require.Equal(t, 1, status.Nodes)
}

func TestSourceApplyRollbackWhenControllerRejects(t *testing.T) {
	m := sourceTestManager(t)
	address := "https://offline.invalid/sub"
	m.saved.URLs = []string{address}
	m.saved.SubscriptionCache = map[string]subscriptionCache{subscriptionID(address): cachedSource("old")}
	require.NoError(t, m.resolveSources(context.Background(), &m.saved, false))
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer controller.Close()
	m.controllerURL = controller.URL
	require.NoError(t, m.Submit("subscription_remove/"+subscriptionID(address), nil, false))
	status := waitSourceOperation(t, m)
	require.Contains(t, status.Error, "previous configuration retained")
	require.Equal(t, 1, status.Subscriptions)
	require.Equal(t, 1, status.Nodes)
}

func TestSubscriptionLifecyclePreservesExisting1024DynamicProxies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("proxies:\n - {name: subscription-node, type: http, server: localhost, port: 9000}\n"))
	}))
	defer server.Close()
	m := sourceTestManager(t)
	m.saved.DownloadMode = SubscriptionDownloadDirect
	for i := 0; i < 1024; i++ {
		raw := fmt.Sprintf("http://fixture-%d:test-password@localhost:1080", i)
		node, err := dynamicProxyNode(raw)
		require.NoError(t, err)
		m.saved.DynamicProxies = append(m.saved.DynamicProxies, raw)
		m.saved.Nodes = append(m.saved.Nodes, node)
	}
	original := append([]string{}, m.saved.DynamicProxies...)
	require.NoError(t, m.SubmitSourceManagement("subscription_add", []string{server.URL}, nil, false, "Test subscription"))
	status := waitSourceOperation(t, m)
	require.Empty(t, status.Error)
	require.Equal(t, 1, status.Subscriptions)
	require.Equal(t, 1024, status.DynamicProxies)
	require.Equal(t, 1025, status.Nodes)
	for _, action := range []string{"subscription_refresh", "subscription_disable", "subscription_enable", "subscription_remove"} {
		require.NoError(t, m.Submit(action+"/"+subscriptionID(server.URL), nil, false))
		status = waitSourceOperation(t, m)
		require.Empty(t, status.Error, action)
		require.Equal(t, original, m.saved.DynamicProxies, action)
		require.Equal(t, 1024, status.DynamicProxies)
	}
	require.Zero(t, status.Subscriptions)
	require.Equal(t, 1024, status.Nodes)
}
