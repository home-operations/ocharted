package sign

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/home-operations/ocharted/internal/oci"
)

func testKeyPEM(t *testing.T) ([]byte, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), pub
}

func TestLoadRejectsNonEd25519(t *testing.T) {
	if _, err := Load([]byte("not pem")); err == nil {
		t.Fatal("expected error for non-PEM input")
	}

	// An ECDSA key must be rejected by type, with the determinism rationale.
	ecdsaPEM := `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgS7v0Sk1PJZGxLnCm
XHkKYleyeVIN80mQpXAcSanhX7uhRANCAAQZW4c2p2fjRAybQGYUxHIitCk8fPMy
FdCA1nOTz2SqEAqBEHSjqBEuTAdiBhtJBLpN3PW9DcQRSl2PM2SwLIzc
-----END PRIVATE KEY-----`
	_, err := Load([]byte(ecdsaPEM))
	if err == nil || !strings.Contains(err.Error(), "Ed25519") {
		t.Fatalf("expected Ed25519-only rejection, got %v", err)
	}
}

func TestArtifactDeterministicAndVerifiable(t *testing.T) {
	keyPEM, pub := testKeyPEM(t)
	signer, err := Load(keyPEM)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	ref := "ocharted.example.com/charts.jetstack.io/cert-manager"
	target := "sha256:" + strings.Repeat("ab", 32)

	a, err := signer.Artifact(ref, target)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	b, err := signer.Artifact(ref, target)
	if err != nil {
		t.Fatalf("Artifact (second): %v", err)
	}
	if a.ManifestDigest != b.ManifestDigest || string(a.Manifest) != string(b.Manifest) {
		t.Fatal("signature artifact is not deterministic")
	}

	var m oci.Manifest
	if err := json.Unmarshal(a.Manifest, &m); err != nil {
		t.Fatalf("manifest decode: %v", err)
	}
	if len(m.Layers) != 1 || m.Layers[0].MediaType != payloadMediaType {
		t.Fatalf("unexpected layers: %+v", m.Layers)
	}
	if m.Layers[0].Digest != a.PayloadDigest || m.Config.Digest != a.ConfigDigest {
		t.Fatal("manifest descriptors do not match derived blobs")
	}

	sig, err := base64.StdEncoding.DecodeString(m.Layers[0].Annotations[sigAnnotation])
	if err != nil {
		t.Fatalf("signature annotation decode: %v", err)
	}
	if !ed25519.Verify(pub, a.Payload, sig) {
		t.Fatal("signature does not verify over the payload")
	}

	var payload map[string]any
	if err := json.Unmarshal(a.Payload, &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	critical := payload["critical"].(map[string]any)
	if critical["image"].(map[string]any)["docker-manifest-digest"] != target {
		t.Fatalf("payload digest claim wrong: %v", critical)
	}
	if critical["identity"].(map[string]any)["docker-reference"] != ref {
		t.Fatalf("payload reference wrong: %v", critical)
	}

	if data, mt, ok := a.Blob(a.PayloadDigest); !ok || mt != payloadMediaType || string(data) != string(a.Payload) {
		t.Fatal("Blob lookup for payload failed")
	}
	if _, _, ok := a.Blob("sha256:" + strings.Repeat("0", 64)); ok {
		t.Fatal("Blob matched a foreign digest")
	}
}

func TestPublicKeyPEMRoundTrip(t *testing.T) {
	keyPEM, pub := testKeyPEM(t)
	signer, err := Load(keyPEM)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out, err := signer.PublicKeyPEM()
	if err != nil {
		t.Fatalf("PublicKeyPEM: %v", err)
	}
	block, _ := pem.Decode(out)
	if block == nil || block.Type != "PUBLIC KEY" {
		t.Fatalf("unexpected PEM output: %s", out)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	if !pub.Equal(parsed.(ed25519.PublicKey)) {
		t.Fatal("public key round trip mismatch")
	}
}
