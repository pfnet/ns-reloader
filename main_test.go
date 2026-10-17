// Copyright 2026 owenshen0907.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestOIDCAuthProvider(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Authorization"))
	}))
	defer server.Close()

	// Only token expiry is inspected by client-go. No identity provider or real credentials are needed.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	claims := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix())),
	)
	token := header + "." + claims + ".test-signature"
	client, err := rest.HTTPClientFor(&rest.Config{
		Host: server.URL,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}),
		},
		AuthProvider: &clientcmdapi.AuthProviderConfig{
			Name: "oidc",
			Config: map[string]string{
				"idp-issuer-url": "https://issuer.example.invalid",
				"client-id":      "ns-reloader-test",
				"id-token":       token,
			},
		},
	})
	require.NoError(t, err)

	response, err := client.Get(server.URL)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "Bearer "+token, string(body))
}
