// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"errors"
	"testing"

	"dario.cat/mergo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMirrorFqdn = "mymirror.azurecr.io"

func TestRewriteMirrorableValues_MirrorableImageReference(t *testing.T) {
	values := map[string]any{
		"image": MirrorableImageReference("mcr.microsoft.com/foo/bar:1.2.3"),
	}

	images := rewriteMirrorableValues(values, testMirrorFqdn)

	assert.Equal(t, []sourceImage{
		{Registry: "mcr.microsoft.com", Repository: "foo/bar", Tag: "1.2.3"},
	}, images)
	assert.Equal(t, "mymirror.azurecr.io/tyger/foo/bar:1.2.3", values["image"])
}

func TestRewriteMirrorableValues_MirrorableImageReferenceWithDigest(t *testing.T) {
	values := map[string]any{
		"image": MirrorableImageReference("mcr.microsoft.com/foo/bar@sha256:" +
			"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"),
	}

	images := rewriteMirrorableValues(values, testMirrorFqdn)

	assert.Equal(t, []sourceImage{
		{Registry: "mcr.microsoft.com", Repository: "foo/bar", Tag: "sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
	}, images)
	assert.Equal(t,
		"mymirror.azurecr.io/tyger/foo/bar@sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		values["image"])
}

func TestImportPaths_Tag(t *testing.T) {
	paths := importPaths("foo/bar", "1.2.3", "tyger/foo/bar")

	assert.Equal(t, acrImportPaths{
		SourceImage: "foo/bar:1.2.3",
		TargetTag:   "tyger/foo/bar:1.2.3",
	}, paths)
}

func TestImportPaths_Digest(t *testing.T) {
	paths := importPaths("foo/bar", "sha256:deadbeef", "tyger/foo/bar")

	assert.Equal(t, acrImportPaths{
		SourceImage:               "foo/bar@sha256:deadbeef",
		TargetRepositoryForDigest: "tyger/foo/bar",
	}, paths)
}

func TestMirrorValidationPostRenderer_ReturnsOriginalManifest(t *testing.T) {
	buffer := bytes.NewBufferString("rendered manifest")
	var validatedManifest string
	renderer := mirrorValidationPostRenderer{
		validate: func(manifest string) error {
			validatedManifest = manifest
			return nil
		},
	}

	result, err := renderer.Run(buffer)

	require.NoError(t, err)
	assert.Same(t, buffer, result)
	assert.Equal(t, "rendered manifest", validatedManifest)
}

func TestMirrorValidationPostRenderer_ReturnsValidationError(t *testing.T) {
	validationErr := errors.New("validation failed")
	renderer := mirrorValidationPostRenderer{
		validate: func(string) error {
			return validationErr
		},
	}

	result, err := renderer.Run(bytes.NewBufferString("rendered manifest"))

	require.ErrorIs(t, err, validationErr)
	assert.Nil(t, result)
}

func TestShouldMirrorChart_TrueWhenChartRefUnchanged(t *testing.T) {
	config := &HelmChartConfig{
		ChartRef: "traefik/traefik",
		Version:  "24.0.0",
		RepoUrl:  "https://custom.example/helm",
	}

	assert.True(t, shouldMirrorChart(config, "traefik/traefik"))
}

func TestShouldMirrorChart_FalseWhenChartRefOverridden(t *testing.T) {
	config := &HelmChartConfig{
		ChartRef: "custom/traefik",
		Version:  "24.0.0",
		RepoUrl:  "https://custom.example/helm",
	}

	assert.False(t, shouldMirrorChart(config, "traefik/traefik"))
}

func TestPreserveMirrorableValueTypes_OverriddenDefaultsAreMirrored(t *testing.T) {
	defaults := &HelmChartConfig{Values: map[string]any{
		"image": map[string]any{
			"repository": MirrorableQualifiedRepository("mcr.microsoft.com/azurelinux/base/default"),
			"tag":        MirrorableTag("1.0"),
		},
	}}
	overrides := &HelmChartConfig{Values: map[string]any{
		"image": map[string]any{
			"repository": "ghcr.io/acme/custom",
			"tag":        "2.0",
		},
	}}
	overridesForMerge := cloneHelmChartConfig(overrides)
	preserveMirrorableValueTypes(defaults.Values, overridesForMerge.Values)

	require.NoError(t, mergo.Merge(defaults, overridesForMerge, mergo.WithOverride))
	images := rewriteMirrorableValues(defaults.Values, testMirrorFqdn)

	assert.Equal(t, []sourceImage{
		{Registry: "ghcr.io", Repository: "acme/custom", Tag: "2.0"},
	}, images)
	image := defaults.Values["image"].(map[string]any)
	assert.Equal(t, "mymirror.azurecr.io/tyger/acme/custom", image["repository"])
	assert.Equal(t, "2.0", image["tag"])

	originalOverrideImage := overrides.Values["image"].(map[string]any)
	assert.IsType(t, "", originalOverrideImage["repository"])
	assert.IsType(t, "", originalOverrideImage["tag"])
}

func TestPreserveMirrorableValueTypes_UserAddedValuesRemainPlain(t *testing.T) {
	defaults := map[string]any{
		"image": MirrorableImageReference("mcr.microsoft.com/foo/bar:1.0"),
	}
	overrides := map[string]any{
		"extraImage": "example.com/extra/image:2.0",
	}

	preserveMirrorableValueTypes(defaults, overrides)

	assert.IsType(t, "", overrides["extraImage"])
}

func TestRewriteMirrorableValues_QualifiedRepositoryAndTag(t *testing.T) {
	values := map[string]any{
		"image": map[string]any{
			"repository": MirrorableQualifiedRepository("mcr.microsoft.com/azurelinux/base/cert-manager-controller"),
			"tag":        MirrorableTag("1.2.3"),
		},
	}

	images := rewriteMirrorableValues(values, testMirrorFqdn)

	assert.Equal(t, []sourceImage{
		{Registry: "mcr.microsoft.com", Repository: "azurelinux/base/cert-manager-controller", Tag: "1.2.3"},
	}, images)
	img := values["image"].(map[string]any)
	assert.Equal(t, "mymirror.azurecr.io/tyger/azurelinux/base/cert-manager-controller", img["repository"])
	assert.Equal(t, "1.2.3", img["tag"])
}

func TestRewriteMirrorableValues_RegistryRepositoryTagSplit(t *testing.T) {
	values := map[string]any{
		"image": map[string]any{
			"registry":   MirrorableRegistry("mcr.microsoft.com"),
			"repository": MirrorableRepository("oss/traefik/traefik"),
			"tag":        MirrorableTag("v2.10.7"),
		},
	}

	images := rewriteMirrorableValues(values, testMirrorFqdn)

	assert.Equal(t, []sourceImage{
		{Registry: "mcr.microsoft.com", Repository: "oss/traefik/traefik", Tag: "v2.10.7"},
	}, images)
	img := values["image"].(map[string]any)
	assert.Equal(t, "mymirror.azurecr.io", img["registry"])
	assert.Equal(t, "tyger/oss/traefik/traefik", img["repository"])
	assert.Equal(t, "v2.10.7", img["tag"])
}

func TestRewriteMirrorableValues_NestedMaps(t *testing.T) {
	values := map[string]any{
		"cert-manager": map[string]any{
			"image": map[string]any{
				"repository": MirrorableQualifiedRepository("mcr.microsoft.com/azurelinux/base/cert-manager-controller"),
				"tag":        MirrorableTag("1.2.3"),
			},
			"webhook": map[string]any{
				"image": map[string]any{
					"repository": MirrorableQualifiedRepository("mcr.microsoft.com/azurelinux/base/cert-manager-webhook"),
					"tag":        MirrorableTag("1.2.3"),
				},
			},
		},
	}

	images := rewriteMirrorableValues(values, testMirrorFqdn)

	assert.ElementsMatch(t, []sourceImage{
		{Registry: "mcr.microsoft.com", Repository: "azurelinux/base/cert-manager-controller", Tag: "1.2.3"},
		{Registry: "mcr.microsoft.com", Repository: "azurelinux/base/cert-manager-webhook", Tag: "1.2.3"},
	}, images)
}

func TestRewriteMirrorableValues_RecurseIntoSlicesOfMaps(t *testing.T) {
	values := map[string]any{
		"deployment": map[string]any{
			"additionalContainers": []any{
				map[string]any{
					"name":  "sidecar",
					"image": MirrorableImageReference("mcr.microsoft.com/foo/bar:1.0"),
				},
			},
		},
	}

	images := rewriteMirrorableValues(values, testMirrorFqdn)

	assert.Equal(t, []sourceImage{
		{Registry: "mcr.microsoft.com", Repository: "foo/bar", Tag: "1.0"},
	}, images)
	container := values["deployment"].(map[string]any)["additionalContainers"].([]any)[0].(map[string]any)
	assert.Equal(t, "mymirror.azurecr.io/tyger/foo/bar:1.0", container["image"])
}

func TestRewriteMirrorableValues_NoMirrorableValues(t *testing.T) {
	values := map[string]any{
		"image": map[string]any{
			"repository": "plain-string-not-mirrored",
			"tag":        "1.0",
		},
		"other": 42,
	}

	images := rewriteMirrorableValues(values, testMirrorFqdn)

	assert.Empty(t, images)
	img := values["image"].(map[string]any)
	assert.Equal(t, "plain-string-not-mirrored", img["repository"])
	assert.Equal(t, "1.0", img["tag"])
}

func TestRewriteMirrorableValues_LoneTagNotReportedAsImage(t *testing.T) {
	// A MirrorableTag without an accompanying repository/registry in the same
	// map is not a complete image group, so no import is recorded. The value
	// is left in the map (will serialize as its underlying string).
	values := map[string]any{
		"image": map[string]any{
			"tag": MirrorableTag("1.0"),
		},
	}

	images := rewriteMirrorableValues(values, testMirrorFqdn)

	assert.Empty(t, images)
}

func TestRewriteMirrorableValues_NilValuesIsNoOp(t *testing.T) {
	images := rewriteMirrorableValues(nil, testMirrorFqdn)
	assert.Empty(t, images)
}

func TestExtractManifestImages(t *testing.T) {
	manifest := `apiVersion: v1
kind: Pod
metadata:
  name: a
spec:
  initContainers:
    - name: init
      image: registry.example.com/lib/init:1.0
  containers:
    - name: app
      image: registry.example.com/lib/app:2.0
    - name: side
      image: registry.example.com/lib/app:2.0
---
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - name: c
          image: other.example.com/foo:3.0
`
	images := extractManifestImages(manifest)
	assert.Equal(t, []string{
		"other.example.com/foo:3.0",
		"registry.example.com/lib/app:2.0",
		"registry.example.com/lib/init:1.0",
	}, images)
}

func TestExtractManifestImages_IgnoresNonStringImageFields(t *testing.T) {
	// "image:" with a sub-map (e.g. helm values shape embedded in a
	// ConfigMap) should not be treated as a leaf image string.
	manifest := `apiVersion: v1
kind: ConfigMap
data:
  values: |
    foo: bar
spec:
  image:
    repository: foo
    tag: bar
`
	images := extractManifestImages(manifest)
	assert.Empty(t, images)
}

func TestParseFullImageRef(t *testing.T) {
	cases := []struct {
		name     string
		ref      string
		registry string
		repo     string
		tag      string
		ok       bool
	}{
		{"tag", "mcr.microsoft.com/foo/bar:1.2.3", "mcr.microsoft.com", "foo/bar", "1.2.3", true},
		{"digest", "mcr.microsoft.com/foo/bar@sha256:abc", "mcr.microsoft.com", "foo/bar", "sha256:abc", true},
		{"port-in-registry", "registry:5000/foo:1.0", "registry:5000", "foo", "1.0", true},
		{"no-slash", "foo:1.0", "", "", "", false},
		{"no-tag", "mcr.microsoft.com/foo/bar", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, p, tg, ok := parseFullImageRef(tc.ref)
			assert.Equal(t, tc.ok, ok)
			if ok {
				assert.Equal(t, tc.registry, r)
				assert.Equal(t, tc.repo, p)
				assert.Equal(t, tc.tag, tg)
			}
		})
	}
}
