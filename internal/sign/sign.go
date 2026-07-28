// Package sign derives cosign-compatible signature artifacts for served
// manifests, on demand. A signature here attests the path — "these bytes came
// through this ocify instance, unmodified" — not upstream authorship; Flux's
// `verify.provider: cosign` turns that into an enforceable admission gate.
//
// Only Ed25519 keys are accepted, deliberately: Ed25519 signing is
// deterministic, so a signature is — like everything else ocify serves — a
// pure function of its inputs, and any replica re-derives byte-identical
// signature blobs. Default ECDSA would produce different bytes per invocation
// (random nonce) and silently break by-digest blob serving across replicas.
package sign

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/home-operations/ocify/internal/oci"
)

// Media types of a cosign signature artifact.
const (
	payloadMediaType = "application/vnd.dev.cosign.simplesigning.v1+json"
	configMediaType  = "application/vnd.oci.image.config.v1+json"
	// sigAnnotation carries the base64 signature on the payload layer
	// descriptor, per the cosign convention.
	sigAnnotation = "dev.cosignproject.cosign/signature"
)

// Signer signs served manifests with a single Ed25519 key.
type Signer struct {
	priv ed25519.PrivateKey
}

// Signature is a fully derived cosign signature artifact: the manifest served
// under the sha256-<digest>.sig tag, plus its two blobs.
type Signature struct {
	Manifest       []byte
	ManifestDigest string

	Payload       []byte
	PayloadDigest string

	Config       []byte
	ConfigDigest string
}

// Blob returns the blob content matching digest, if this signature holds it.
func (s *Signature) Blob(digest string) ([]byte, string, bool) {
	switch digest {
	case s.PayloadDigest:
		return s.Payload, payloadMediaType, true
	case s.ConfigDigest:
		return s.Config, configMediaType, true
	}
	return nil, "", false
}

// Load parses a PEM-encoded PKCS#8 Ed25519 private key (e.g. generated with
// `openssl genpkey -algorithm ed25519`). Other key types are rejected — see
// the package comment for why determinism is non-negotiable here.
func Load(pemBytes []byte) (*Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("sign: signing key is not PEM-encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sign: parse signing key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("sign: signing key is %T; only Ed25519 is supported (deterministic signatures are required for stateless replicas)", key)
	}
	return &Signer{priv: priv}, nil
}

// PublicKeyPEM returns the verification key in the PEM form `cosign verify
// --key` and Flux's cosign secretRef expect.
func (s *Signer) PublicKeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(s.priv.Public())
	if err != nil {
		return nil, fmt.Errorf("sign: marshal public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// simpleSigning is cosign's SimpleSigning payload. Field order matches
// cosign's own structs so the canonical bytes do too.
type simpleSigning struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
	Optional map[string]any `json:"optional"`
}

// Artifact derives the signature artifact for the target manifest digest, as
// referenced by ref (host/name as the client addresses it). Pure function of
// (key, ref, targetDigest): byte-identical on every call, every replica.
func (s *Signer) Artifact(ref, targetDigest string) (*Signature, error) {
	var p simpleSigning
	p.Critical.Identity.DockerReference = ref
	p.Critical.Image.DockerManifestDigest = targetDigest
	p.Critical.Type = "cosign container image signature"

	payload, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("sign: marshal payload: %w", err)
	}
	sig := ed25519.Sign(s.priv, payload)

	out := &Signature{
		Payload:       payload,
		PayloadDigest: oci.Digest(payload),
	}

	// A minimal image config for the signature "image", mirroring the shape
	// cosign builds (zero timestamps, the payload layer as the sole diff).
	// Verifiers read the manifest and payload; the config exists so a client
	// that does fetch it gets something valid.
	config := fmt.Sprintf(
		`{"architecture":"","created":"0001-01-01T00:00:00Z","history":[{"created":"0001-01-01T00:00:00Z"}],"os":"","rootfs":{"type":"layers","diff_ids":[%q]},"config":{}}`,
		out.PayloadDigest,
	)
	out.Config = []byte(config)
	out.ConfigDigest = oci.Digest(out.Config)

	manifest := oci.Manifest{
		SchemaVersion: 2,
		MediaType:     oci.ManifestMediaType,
		Config: oci.Descriptor{
			MediaType: configMediaType,
			Digest:    out.ConfigDigest,
			Size:      int64(len(out.Config)),
		},
		Layers: []oci.Descriptor{{
			MediaType:   payloadMediaType,
			Digest:      out.PayloadDigest,
			Size:        int64(len(payload)),
			Annotations: map[string]string{sigAnnotation: base64.StdEncoding.EncodeToString(sig)},
		}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("sign: marshal signature manifest: %w", err)
	}
	out.Manifest = raw
	out.ManifestDigest = oci.Digest(raw)
	return out, nil
}
