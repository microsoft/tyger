package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodespecValidationWithValidCodespec(t *testing.T) {
	require := require.New(t)

	c := Codespec{Image: "x"}
	require.Nil(c.Validate())
	c.Buffers = &BufferParameters{
		Inputs:  []string{"a"},
		Outputs: []string{"b"},
	}
}

func TestCodespecValidationMissingImage(t *testing.T) {
	require := require.New(t)

	c := Codespec{}
	require.NotNil(c.Validate())
}

func TestCodespecValidationDuplicatedBufferNames(t *testing.T) {
	require := require.New(t)

	c := Codespec{
		Image: "x",
		Buffers: &BufferParameters{
			Inputs:  []string{"a"},
			Outputs: []string{"A"},
		},
	}
	require.NotNil(c.Validate())
}

func TestCodespecValidationBufferNameCannotBeEmpty(t *testing.T) {
	require := require.New(t)

	c := Codespec{
		Image: "x",
		Buffers: &BufferParameters{
			Inputs: []string{""},
		},
	}
	require.NotNil(c.Validate())
}
