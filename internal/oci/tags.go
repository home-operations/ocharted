package oci

import (
	"regexp"
	"strings"
)

// tagPattern is the OCI distribution spec's valid-tag grammar.
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

// VersionToTag maps a Helm chart version to an OCI tag. SemVer build metadata
// uses '+', which is not a legal tag character; Helm's own OCI convention
// substitutes '_', and both directions here rely on SemVer forbidding a
// literal underscore so the mapping is lossless.
func VersionToTag(version string) string {
	return strings.ReplaceAll(version, "+", "_")
}

// TagToVersion maps an OCI tag back to the Helm chart version it was derived
// from.
func TagToVersion(tag string) string {
	return strings.ReplaceAll(tag, "_", "+")
}

// ValidTag reports whether the chart version maps to a tag the OCI
// distribution spec accepts. Versions that don't (rare, but nothing stops a
// Helm repo from publishing one) are skipped rather than served broken.
func ValidTag(tag string) bool {
	return tagPattern.MatchString(tag)
}
