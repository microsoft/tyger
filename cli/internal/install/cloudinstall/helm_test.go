// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
