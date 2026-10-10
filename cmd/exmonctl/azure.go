package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// azToken gets an access token for resource from the Azure CLI's signed-in
// account (`az login`). exmonctl never stores credentials: every run asks az,
// which handles MFA, Conditional Access and PIM for us.
func azToken(resource string) (string, error) {
	out, err := exec.Command("az", "account", "get-access-token", "--resource", resource,
		"--query", "accessToken", "--output", "tsv").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("az account get-access-token failed (run `az login` first): %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("run the Azure CLI (az): %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

const keyVaultAPI = "7.4"

// vaultURL turns a vault name or URL into https://<name>.vault.azure.net.
func vaultURL(v string) string {
	if strings.HasPrefix(v, "https://") {
		return strings.TrimRight(v, "/")
	}
	return "https://" + v + ".vault.azure.net"
}

// kvCall makes one Key Vault REST call and decodes the JSON answer into out.
func kvCall(method, u, token string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
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
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		json.Unmarshal(b, &e)
		return fmt.Errorf("Key Vault: HTTP %d %s %s", resp.StatusCode, e.Error.Code, e.Error.Message)
	}
	return json.Unmarshal(b, out)
}

// kvKey is the part of a Key Vault key we use: its versioned id and public point.
type kvKey struct {
	Key struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
	} `json:"key"`
}

// getVaultKey reads the current version of a key.
func getVaultKey(vault, name, token string) (kvKey, *ecdsa.PublicKey, error) {
	var k kvKey
	u := vaultURL(vault) + "/keys/" + url.PathEscape(name) + "?api-version=" + keyVaultAPI
	if err := kvCall(http.MethodGet, u, token, nil, &k); err != nil {
		return k, nil, err
	}
	if !strings.HasPrefix(k.Key.Kty, "EC") || k.Key.Crv != "P-256" {
		return k, nil, fmt.Errorf("key %s is %s %s; exmon needs EC P-256", name, k.Key.Kty, k.Key.Crv)
	}
	x, err1 := base64.RawURLEncoding.DecodeString(k.Key.X)
	y, err2 := base64.RawURLEncoding.DecodeString(k.Key.Y)
	if err1 != nil || err2 != nil {
		return k, nil, errors.New("Key Vault returned a malformed public key")
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	return k, pub, nil
}

// vaultSigner signs with a Key Vault key. The private key never leaves the vault
// (an HSM with the Premium SKU); exmonctl only sends it a SHA-256 digest.
type vaultSigner struct {
	keyID string // the id envelopes carry; matches the .pem file name built into exmon
	kid   string // Key Vault's versioned key URL
	pub   *ecdsa.PublicKey
	token string
}

func newVaultSigner(vault, keyName, keyID string) (*vaultSigner, error) {
	token, err := azToken("https://vault.azure.net")
	if err != nil {
		return nil, err
	}
	k, pub, err := getVaultKey(vault, keyName, token)
	if err != nil {
		return nil, err
	}
	return &vaultSigner{keyID: keyID, kid: k.Key.Kid, pub: pub, token: token}, nil
}

func (v *vaultSigner) KeyID() string { return v.keyID }

func (v *vaultSigner) SignDigest(digest []byte) ([]byte, error) {
	var res struct {
		Value string `json:"value"`
	}
	err := kvCall(http.MethodPost, v.kid+"/sign?api-version="+keyVaultAPI, v.token,
		map[string]string{"alg": "ES256", "value": base64.RawURLEncoding.EncodeToString(digest)}, &res)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(res.Value)
	if err != nil || len(sig) != 64 {
		return nil, errors.New("Key Vault returned an unexpected signature")
	}
	// Check it ourselves before anything is published.
	if !ecdsa.Verify(v.pub, digest, new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("Key Vault's signature doesn't verify against its public key")
	}
	return sig, nil
}
