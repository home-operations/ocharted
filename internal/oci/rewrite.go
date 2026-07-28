package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"go.yaml.in/yaml/v4"
)

// RewriteDependencies rewrites every HTTP(S) dependency repository URL in the
// chart's Chart.yaml to oci://externalHost/<upstream-host[/path]>, so
// dependency resolution (`helm dependency update`) also flows through the
// proxy. Everything else in the tarball — including every tar header — is
// preserved verbatim.
//
// Like all of this package it is a pure function: identical inputs yield
// byte-identical output. externalHost must be a static configured name (never
// a request Host), so every replica rewrites identically no matter which
// hostname a client used. When no dependency needs rewriting the input is
// returned unchanged, byte for byte — charts without HTTP dependencies keep
// full digest fidelity to upstream.
func RewriteDependencies(chart []byte, externalHost string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(chart))
	if err != nil {
		return nil, fmt.Errorf("oci: chart is not a gzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	type entry struct {
		hdr  *tar.Header
		data []byte
	}
	var entries []entry
	rewrote := false

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("oci: read chart tarball: %w", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("oci: read chart entry %s: %w", hdr.Name, err)
		}
		if isChartYAML(hdr.Name) {
			newData, changed, err := rewriteChartYAML(data, externalHost)
			if err != nil {
				return nil, err
			}
			if changed {
				data = newData
				rewrote = true
			}
		}
		entries = append(entries, entry{hdr: hdr, data: data})
	}
	if !rewrote {
		return chart, nil
	}

	// Repack with NoCompression: deflate output is implementation-defined and
	// has changed between Go releases, which would silently shift rewritten
	// digests across ocharted builds; stored-block framing is format-stable, so
	// digests survive toolchain upgrades. Charts are small — the bandwidth
	// cost is noise.
	var buf bytes.Buffer
	out, err := gzip.NewWriterLevel(&buf, gzip.NoCompression)
	if err != nil {
		return nil, fmt.Errorf("oci: gzip writer: %w", err)
	}
	tw := tar.NewWriter(out)
	for _, e := range entries {
		e.hdr.Size = int64(len(e.data))
		if err := tw.WriteHeader(e.hdr); err != nil {
			return nil, fmt.Errorf("oci: write chart entry %s: %w", e.hdr.Name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, fmt.Errorf("oci: write chart entry %s: %w", e.hdr.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("oci: close tarball: %w", err)
	}
	if err := out.Close(); err != nil {
		return nil, fmt.Errorf("oci: close gzip: %w", err)
	}
	return buf.Bytes(), nil
}

// rewriteChartYAML rewrites dependency repository values in Chart.yaml. It
// operates on the YAML node tree, so key order and comments survive and the
// output is deterministic for a given input.
func rewriteChartYAML(raw []byte, externalHost string) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("oci: parse Chart.yaml: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, nil
	}

	changed := false
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "dependencies" || root.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, dep := range root.Content[i+1].Content {
			if dep.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(dep.Content); j += 2 {
				key, val := dep.Content[j], dep.Content[j+1]
				if key.Value != "repository" || val.Kind != yaml.ScalarNode {
					continue
				}
				if rewritten, ok := rewriteRepoURL(val.Value, externalHost); ok {
					val.Value = rewritten
					changed = true
				}
			}
		}
	}
	if !changed {
		return nil, false, nil
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, false, fmt.Errorf("oci: encode Chart.yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, false, fmt.Errorf("oci: encode Chart.yaml: %w", err)
	}
	return buf.Bytes(), true, nil
}

// rewriteRepoURL maps an HTTP(S) Helm repository URL onto the proxy's OCI
// namespace. Everything else — file:// paths, @alias / alias: references,
// already-OCI URLs, empty values — is left alone.
func rewriteRepoURL(repo, externalHost string) (string, bool) {
	if !strings.HasPrefix(repo, "https://") && !strings.HasPrefix(repo, "http://") {
		return "", false
	}
	u, err := url.Parse(repo)
	if err != nil || u.Host == "" {
		return "", false
	}
	name := u.Host + strings.TrimSuffix(u.Path, "/")
	return "oci://" + externalHost + "/" + name, true
}
