// Package testchart builds minimal Helm chart tarballs in memory for tests.
package testchart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"maps"
	"slices"
)

// Tgz returns a gzipped chart tarball containing <name>/Chart.yaml with the
// given rendered YAML plus any extra files (path → content, relative to the
// chart root). All headers are zero-timestamped so output is reproducible.
func Tgz(name, chartYAML string, extra map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	write := func(path, content string) {
		hdr := &tar.Header{
			Name: name + "/" + path,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			panic(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			panic(err)
		}
	}

	write("Chart.yaml", chartYAML)
	for _, p := range slices.Sorted(maps.Keys(extra)) {
		write(p, extra[p])
	}

	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// ChartYAML renders a minimal Chart.yaml for name/version.
func ChartYAML(name, version string) string {
	return fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\ndescription: test chart\n", name, version)
}
