package clicontext

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type CliContext struct {
	ServerUri string `yaml:"serverUri"`
}

func contextPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to locate home directory: %v", err)
	}
	return filepath.Join(home, ".tyger"), nil
}

func WriteCliContext(context CliContext) error {
	path, err := contextPath()
	if err == nil {
		var bytes []byte
		bytes, err = yaml.Marshal(context)
		if err == nil {
			err = ioutil.WriteFile(path, bytes, 0644)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to write context: %v", err)
	}

	return nil
}

func GetCliContext() (CliContext, error) {
	context := CliContext{}
	path, err := contextPath()
	if err != nil {
		return context, err
	}
	var bytes []byte
	bytes, err = ioutil.ReadFile(path)
	if err != nil {
		return context, err
	}

	err = yaml.Unmarshal(bytes, &context)
	return context, err
}

func (c CliContext) Validate() error {
	if c.ServerUri == "" {
		return errors.New("run tyger login")
	}

	resp, err := http.Get(fmt.Sprintf("%s/healthcheck", c.ServerUri))
	if err != nil || resp.StatusCode != http.StatusOK {
		return errors.New("the server URL does not appear to point to a valid tyger server")
	}

	return nil
}
