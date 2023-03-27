//go:build integrationtest

package integrationtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane/model"
	"github.com/andreyvit/diff"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	stdout, stderr, err := runTyger("login", "status")
	if err != nil {
		fmt.Fprintln(os.Stderr, stderr, stdout)
		log.Fatal(err)
	}
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
}

func TestEndToEnd(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	// create a codespec
	const codespecName = "testcodespec"

	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", "curlimages/curl",
		"--command",
		"--",
		"sh", "-c",
		`
		set -euo pipefail
		inp=$(cat "$INPUT_PIPE")
		echo "${inp}: Bonjour" > "$OUTPUT_PIPE"
		curl --fail "${MRD_STORAGE_URI}/healthcheck"
		`,
	)

	// create an input buffer and a SAS token to be able to write to it
	inputBufferId := runTygerSuceeds(t, "buffer", "create")
	inputSasUri := runTygerSuceeds(t, "buffer", "access", inputBufferId, "-w")

	// create and output buffer and a SAS token to be able to read from it
	outputBufferId := runTygerSuceeds(t, "buffer", "create")
	outputSasUri := runTygerSuceeds(t, "buffer", "access", outputBufferId)

	runCommandSuceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputSasUri))

	// create run
	runId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m",
		"-b", fmt.Sprintf("input=%s", inputBufferId),
		"-b", fmt.Sprintf("output=%s", outputBufferId))

	waitForRunSuccess(t, runId)

	output := runCommandSuceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputSasUri))

	require.Equal("Hello: Bonjour", output)
}

func TestEndToEndWithAutomaticallyCreatedBuffers(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	// create a codespec
	const codespecName = "testcodespecwithbuffercreation"

	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", "curlimages/curl",
		"--command",
		"--",
		"sh", "-c",
		`
		set -euo pipefail
		inp=$(cat "$INPUT_PIPE")
		echo "${inp}: Bonjour" > "$OUTPUT_PIPE"
		curl --fail "${MRD_STORAGE_URI}/healthcheck"
		`,
	)

	// create run
	runId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")

	runJson := runTygerSuceeds(t, "run", "show", runId)

	var run model.Run
	require.NoError(json.Unmarshal([]byte(runJson), &run))

	inputBufferId := run.Job.Buffers["input"]
	outputBufferId := run.Job.Buffers["output"]

	runCommandSuceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputBufferId))

	waitForRunSuccess(t, runId)

	output := runCommandSuceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputBufferId))

	require.Equal("Hello: Bonjour", output)
}

func TestEndToEndWithYamlSpecAndAutomaticallyCreatedBuffers(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	runSpec := `
job:
  codespec:
    image: curlimages/curl
    buffers:
      inputs: ["input"]
      outputs: ["output"]
    command:
      - "sh"
      - "-c"
      - |
        set -euo pipefail
        inp=$(cat "$INPUT_PIPE")
        echo "${inp}: Bonjour" > "$OUTPUT_PIPE"
        curl --fail "${MRD_STORAGE_URI}/healthcheck"
timeoutSeconds: 600`

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(ioutil.WriteFile(runSpecPath, []byte(runSpec), 0644))

	// create run
	runId := runTygerSuceeds(t, "run", "create", "--file", runSpecPath)

	runJson := runTygerSuceeds(t, "run", "show", runId)

	var run model.Run
	require.NoError(json.Unmarshal([]byte(runJson), &run))

	inputBufferId := run.Job.Buffers["input"]
	inputSasUri := runTygerSuceeds(t, "buffer", "access", inputBufferId, "-w")
	outputBufferId := run.Job.Buffers["output"]
	outputSasUri := runTygerSuceeds(t, "buffer", "access", outputBufferId)

	runCommandSuceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputSasUri))

	waitForRunSuccess(t, runId)

	output := runCommandSuceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputSasUri))

	require.Equal("Hello: Bonjour", output)
}

func TestEndToEndExecWithYamlSpec(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	runSpec := `
job:
  codespec:
    image: curlimages/curl
    buffers:
      inputs: ["input"]
      outputs: ["output"]
    command:
      - "sh"
      - "-c"
      - |
        set -euo pipefail
        inp=$(cat "$INPUT_PIPE")
        echo -n "${inp}: Bonjour" > "$OUTPUT_PIPE"
        curl --fail "${MRD_STORAGE_URI}/healthcheck"
timeoutSeconds: 600`

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(ioutil.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execCommand := exec.Command("tyger", "run", "exec", "--file", runSpecPath, "--log-level", "trace")
	execCommand.Stdin = bytes.NewReader([]byte("Hello"))
	execStdErr := bytes.NewBuffer(nil)
	execCommand.Stderr = execStdErr

	execStdOut, err := execCommand.Output()
	t.Log(string(execStdErr.Bytes()))
	require.NoError(err)
	require.Equal("Hello: Bonjour", string(execStdOut))
}

func TestEndToEndExecWithYamlWithExistingCodespec(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespecName := strings.ToLower(t.Name())
	version := runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", "curlimages/curl",
		"--command",
		"--",
		"sh", "-c",
		`
		set -euo pipefail
		inp=$(cat "$INPUT_PIPE")
		echo -n "${inp}: Bonjour" > "$OUTPUT_PIPE"
		curl --fail "${MRD_STORAGE_URI}/healthcheck"
		`,
	)

	runSpec := fmt.Sprintf(`
job:
  codespec: %s/versions/%s
timeoutSeconds: 600`, codespecName, version)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(ioutil.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execCommand := exec.Command("tyger", "run", "exec", "--file", runSpecPath, "--log-level", "trace")
	execCommand.Stdin = bytes.NewReader([]byte("Hello"))
	execStdErr := bytes.NewBuffer(nil)
	execCommand.Stderr = execStdErr

	execStdOut, err := execCommand.Output()
	t.Log(string(execStdErr.Bytes()))
	require.NoError(err)
	require.Equal("Hello: Bonjour", string(execStdOut))
}

func TestEndToEndWhenPipesAreNotTouched(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	runSpec := `
job:
  codespec:
    image: curlimages/curl
    buffers:
      inputs: ["input"]
      outputs: ["output"]
    command:
      - "sh"
      - "-c"
      - |
        set -euo pipefail
        echo "hello world"
timeoutSeconds: 600`

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(ioutil.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execCommand := exec.Command("tyger", "run", "exec", "--file", runSpecPath, "--log-level", "trace")
	execCommand.Stdin = bytes.NewReader([]byte("Hello"))
	execStdErr := bytes.NewBuffer(nil)
	execCommand.Stderr = execStdErr

	execStdOut, err := execCommand.Output()
	t.Log(string(execStdErr.Bytes()))
	require.NoError(err)
	require.Empty(string(execStdOut))
}

func TestInvalidCodespecNames(t *testing.T) {
	testCases := []struct {
		name  string
		valid bool
	}{
		{"foo", true},
		{"foo-1_2.9", true},
		{"Foo", false},
		{"foo bar", false},
	}
	for _, tC := range testCases {
		t.Run(tC.name, func(t *testing.T) {
			_, stdErr, err := runTyger("codespec", "create", tC.name, "--image", "busybox")
			if tC.valid {
				assert.Nil(t, err)
			} else {
				assert.NotNil(t, err)
				assert.Contains(t, stdErr, "codespec name")
			}

			newCodespec := model.Codespec{Kind: "worker", Image: "busybox"}
			_, err = controlplane.InvokeRequest(http.MethodPut, fmt.Sprintf("v1/codespecs/%s", tC.name), newCodespec, nil)
			if tC.valid {
				assert.Nil(t, err)
			} else {
				assert.NotNil(t, err)
			}
		})
	}
}

func TestCodespecNameRequirements(t *testing.T) {
	runTyger("codespec", "create", "Foo", "--image", "busybox")
}

// Verify that a run using a codespec that requires a GPU
// is scheduled on a node with one.
func TestGpuResourceRequirement(t *testing.T) {
	t.Parallel()

	const codespecName = "gputestcodespec"
	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "nvidia/cuda:11.0.3-base-ubuntu20.04",
		"--gpu", "1",
		"--command",
		"--",
		"bash", "-c", "[[ $(nvidia-smi -L | wc -l) == 1 ]]") // verify that a GPU is available

	// create run
	runId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")

	run := waitForRunSuccess(t, runId)

	require.NoError(t, json.Unmarshal([]byte(runTygerSuceeds(t, "run", "show", runId)), &run))
	assert.NotEmpty(t, run.Cluster)
	assert.Equal(t, "gpunp", run.Job.NodePool)
}

// Verify that a run using a codespec that does not require a GPU
// is not scheduled on a node with one.
func TestNoGpuResourceRequirement(t *testing.T) {
	t.Parallel()

	const codespecName = "nogputestcodespec"
	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "nvidia/cuda:11.0.3-base-ubuntu20.04",
		"--command",
		"--",
		"bash", "-c", "[[ ! $(nvidia-smi) ]]") // verify that no GPU is available

	// create run
	runId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")

	waitForRunSuccess(t, runId)
}

func TestTargetGpuNodePool(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())
	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "nvidia/cuda:11.0.3-base-ubuntu20.04",
		"--command",
		"--",
		"bash", "-c", "[[ $(nvidia-smi -L | wc -l) == 1 ]]") // verify that a GPU is available

	// create run
	runId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--node-pool", "gpunp", "--timeout", "20m")

	waitForRunSuccess(t, runId)
}

func TestTargetCpuNodePool(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())
	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "nvidia/cuda:11.0.3-base-ubuntu20.04",
		"--command",
		"--",
		"bash", "-c", "[[ ! $(nvidia-smi) ]]") // verify that no GPU is available

	// create run
	runId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--node-pool", "cpunp", "--timeout", "10m")

	waitForRunSuccess(t, runId)
}

func TestTargetingInvalidClusterReturnsError(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())
	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "ubuntu")

	_, stderr, _ := runTyger("run", "create", "--codespec", codespecName, "--cluster", "invalid")
	require.Contains(t, stderr, "Unknown cluster")
}

func TestTargetingInvalidNodePoolReturnsError(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())
	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "ubuntu")

	_, stderr, _ := runTyger("run", "create", "--codespec", codespecName, "--node-pool", "invalid")
	require.Contains(t, stderr, "Unknown nodepool")
}

func TestTargetCpuNodePoolWithGpuResourcesReturnsError(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())
	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "ubuntu",
		"--gpu", "1")

	_, stderr, _ := runTyger("run", "create", "--codespec", codespecName, "--node-pool", "cpunp", "--timeout", "10m")
	require.Contains(t, stderr, "does not have GPUs and cannot satisfy GPU request")
}

func TestUnrecognizedFieldsRejected(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"kind": "job", "image": "x"}
	_, err := controlplane.InvokeRequest(http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.Nil(err)

	requestBody["unknownField"] = "y"
	_, err = controlplane.InvokeRequest(http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.NotNil(err)
}

func TestInvalidCodespecDiscriminatorRejected(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"image": "x"}
	_, err := controlplane.InvokeRequest(http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.ErrorContains(err, "Missing discriminator property 'kind'")

	requestBody["kind"] = "missing"
	_, err = controlplane.InvokeRequest(http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.ErrorContains(err, "Invalid value for the property 'kind'. It can be either 'job' or 'worker'")
}

func TestInvalidCodespecMissingRequiredFieldsRejected(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"kind": "job"}
	_, err := controlplane.InvokeRequest(http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.ErrorContains(err, "The image field is required")
}

func TestResponseContainsRequestIdHeader(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	_, stderr, _ := runTyger("codespec", "show", "missing")

	require.Contains(stderr, "Request-Id")
}

func TestOpenApiSpecIsAsExpected(t *testing.T) {
	t.Parallel()
	ctx, err := controlplane.GetCliContext()
	require.Nil(t, err)
	swaggerUri := fmt.Sprintf("%s/swagger/v1/swagger.yaml", ctx.GetServerUri())
	resp, err := controlplane.NewRetryableClient().Get(swaggerUri)
	require.Nil(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	actualBytes, err := io.ReadAll(resp.Body)
	require.Nil(t, err)
	expectedFilePath, err := filepath.Abs("expected_openapi_spec.yaml")
	require.Nil(t, err)
	expectedBytes, err := ioutil.ReadFile(expectedFilePath)
	require.Nil(t, err)

	if a, e := strings.TrimSpace(string(actualBytes)), strings.TrimSpace(string(expectedBytes)); a != e {
		t.Errorf("Result not as expected. To update, run `curl %s > %s`\n\nDiff:%v",
			swaggerUri,
			expectedFilePath,
			diff.LineDiff(e, a))
	}
}

func TestListRunsPaging(t *testing.T) {
	t.Parallel()

	runTygerSuceeds(t,
		"codespec",
		"create", "exitimmediately",
		"--image", "busybox",
		"--command",
		"--",
		"echo", "hi")

	runs := make(map[string]string)
	for i := 0; i < 10; i++ {
		runs[runTygerSuceeds(t, "run", "create", "--codespec", "exitimmediately", "--timeout", "10m")] = ""
	}

	for uri := "v1/runs?limit=5"; uri != ""; {
		page := model.Page[model.Run]{}
		_, err := controlplane.InvokeRequest(http.MethodGet, uri, nil, &page)
		require.Nil(t, err)
		for _, r := range page.Items {
			delete(runs, fmt.Sprint(r.Id))
			if len(runs) == 0 {
				return
			}
		}

		if page.NextLink == "" {
			break
		}

		uri = strings.TrimLeft(page.NextLink, "/")
	}

	require.Empty(t, runs)
}

func TestListCodespecsFromCli(t *testing.T) {
	t.Parallel()
	prefix := strings.ToLower(t.Name()) + "_"
	codespecNames := [4]string{prefix + "kspace_half_sampled", prefix + "4dcardiac", prefix + "zloc_10mm", prefix + "axial_1mm"}
	codespecMap := make(map[string]string)
	for _, name := range codespecNames {
		codespecMap[name] = runTygerSuceeds(t, "codespec", "create", name, "--image", "busybox")
	}
	var results = runTygerSuceeds(t, "codespec", "list", "--prefix", prefix)
	var returnedCodespecs []model.Codespec
	json.Unmarshal([]byte(results), &returnedCodespecs)
	sort.Strings(codespecNames[:])
	var csIdx int = 0
	for _, cs := range returnedCodespecs {
		if _, ok := codespecMap[cs.Name]; ok {
			require.Equal(t, codespecNames[csIdx], cs.Name)
			require.Equal(t, codespecMap[cs.Name], strconv.Itoa(cs.Version))
			csIdx++
		}
	}
	require.Equal(t, len(codespecNames), csIdx)
}

func TestRecreateCodespec(t *testing.T) {
	t.Parallel()
	codespecName := strings.ToLower(t.Name())
	version1 := runTygerSuceeds(t, "codespec", "create", codespecName, "--image", "busybee", "--command", "--", "echo", "hi I am first")
	version2 := runTygerSuceeds(t, "codespec", "create", codespecName, "--image", "busybox", "--gpu", "2", "--memory-request", "2048048", "--env", "os=ubuntu", "--command", "--", "echo", "hi I am latest")
	require.NotEqual(t, version1, version2)

	version3 := runTygerSuceeds(t, "codespec", "create", codespecName, "--image", "busybox", "--gpu", "2", "--memory-request", "2048048", "--env", "os=ubuntu", "--command", "--", "echo", "hi I am latest")
	require.Equal(t, version2, version3)

	version4 := runTygerSuceeds(t, "codespec", "create", codespecName, "--image", "busybox", "--memory-request", "2048048", "--gpu", "2", "--env", "os=ubuntu", "--command", "--", "echo", "hi I am latest")
	require.Equal(t, version3, version4)

	version5 := runTygerSuceeds(t, "codespec", "create", codespecName, "--image", "busybox", "--memory-request", "2048048", "--gpu", "2", "--env", "os=ubuntu", "--env", "platform=highT", "--command", "--", "echo", "hi I am latest")
	version6 := runTygerSuceeds(t, "codespec", "create", codespecName, "--image", "busybox", "--gpu", "2", "--memory-request", "2048048", "--env", "platform=highT", "--env", "os=ubuntu", "--command", "--", "echo", "hi I am latest")
	require.Equal(t, version5, version6)

	version7 := runTygerSuceeds(t, "codespec", "create", codespecName, "--image", "busybox", "--memory-request", "2048048", "--gpu", "2", "--env", "platform=highT", "--env", "os=windows", "--command", "--", "echo", "hi I am latest")
	require.NotEqual(t, version6, version7)
}

func TestListCodespecsPaging(t *testing.T) {
	t.Parallel()
	prefix := strings.ToLower(t.Name()) + "_"
	inputNames := [12]string{"klamath", "allagash", "middlefork", "johnday", "missouri", "riogrande", "chattooga", "loxahatchee", "noatak", "tuolumne", "riogrande", "allagash"}
	expectedNames1 := [5]string{"allagash", "chattooga", "johnday", "klamath", "loxahatchee"}
	expectedNames2 := [5]string{"middlefork", "missouri", "noatak", "riogrande", "tuolumne"}
	for i := range inputNames {
		inputNames[i] = prefix + inputNames[i]
	}
	for i := range expectedNames1 {
		expectedNames1[i] = prefix + expectedNames1[i]
	}
	for i := range expectedNames2 {
		expectedNames2[i] = prefix + expectedNames2[i]
	}

	var returnedNames1, returnedNames2 [5]string
	var expectedIdx, currentKlamathVersion, expectedKlamathVersion int = 0, 0, 0

	codespecs := make(map[string]string)
	for _, name := range inputNames {
		codespecs[name] = runTygerSuceeds(t, "codespec", "create", name, "--image", "busybox")
	}
	require.Equal(t, len(codespecs), 10)

	for uri := fmt.Sprintf("v1/codespecs?limit=5&prefix=%s", prefix); uri != ""; {
		page := model.Page[model.Codespec]{}
		_, err := controlplane.InvokeRequest(http.MethodGet, uri, nil, &page)
		require.Nil(t, err)
		for _, cs := range page.Items {
			if _, ok := codespecs[cs.Name]; ok {
				if expectedIdx < 5 {
					returnedNames1[expectedIdx] = cs.Name
					expectedIdx++
					if cs.Name == prefix+"klamath" {
						currentKlamathVersion = cs.Version
					}
				} else {
					returnedNames2[expectedIdx-5] = cs.Name
					expectedIdx++
				}
			}
			//simulate concurrent codespec update while paging
			if expectedIdx == 6 && expectedKlamathVersion == 0 {
				var tmp = runTygerSuceeds(t, "codespec", "create", prefix+"klamath", "--image", "busybox", "--", "something different")
				expectedKlamathVersion, err = strconv.Atoi(tmp)
				require.Nil(t, err)
				require.Equal(t, expectedKlamathVersion, currentKlamathVersion+1)
			}
			if expectedIdx > 10 {
				require.Fail(t, "Unexpected codespec count")
			}
		}

		if page.NextLink == "" {
			break
		}

		uri = strings.TrimLeft(page.NextLink, "/")
	}

	require.Equal(t, expectedNames1, returnedNames1)
	require.Equal(t, expectedNames2, returnedNames2)
}

func TestListRunsSince(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())

	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "busybox",
		"--command",
		"--",
		"echo", "hi")

	runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	midId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	lastId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")

	midRunJson := runTygerSuceeds(t, "run", "show", midId)
	midRun := model.Run{}
	err := json.Unmarshal([]byte(midRunJson), &midRun)
	require.Nil(t, err)

	listJson := runTygerSuceeds(t, "run", "list", "--since", midRun.CreatedAt.Format(time.RFC3339Nano))
	list := make([]model.Run, 0)
	json.Unmarshal([]byte(listJson), &list)
	require.Greater(t, len(list), 0)
	for _, r := range list {
		require.Greater(t, r.CreatedAt.UnixNano(), midRun.CreatedAt.UnixNano())
	}

	for _, r := range list {
		if fmt.Sprint(r.Id) == lastId {
			return
		}
	}

	require.Fail(t, "last run not found")
}

func TestListCodespecsWithPrefix(t *testing.T) {
	t.Parallel()

	codespecNames := [4]string{"3d_t2_flair", "t1w-1mm-ax", "t1w-0.9mm-sag", "3d_t1_star"}
	codespecMap := make(map[string]string)
	for i := 0; i < 4; i++ {
		codespecMap[codespecNames[i]] = runTygerSuceeds(t, "codespec", "create", codespecNames[i], "--image", "busybox")
	}

	uri := "v1/codespecs?prefix=3d_"
	page := model.Page[model.Codespec]{}
	_, err := controlplane.InvokeRequest(http.MethodGet, uri, nil, &page)
	require.Nil(t, err)
	for _, cs := range page.Items {
		require.Equal(t, strings.HasPrefix(cs.Name, "3d_"), true)
		if _, ok := codespecMap[cs.Name]; ok {
			delete(codespecMap, cs.Name)
		}
	}
	require.Equal(t, len(codespecMap), 2)

	for cs := range codespecMap {
		require.Equal(t, strings.HasPrefix(cs, "t1w-"), true)
	}
}

func TestGetLogsFromPod(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())

	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "busybox",
		"--command",
		"--",
		"sh", "-c", "for i in `seq 1 5`; do echo $i; done; sleep 30")

	runId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	t.Logf("Run ID: %s", runId)

	waitForRunStarted(t, runId)

	// block until we get the first line
	resp, err := controlplane.InvokeRequest(http.MethodGet, fmt.Sprintf("v1/runs/%s/logs?follow=true", runId), nil, nil)
	require.Nil(t, err)
	reader := bufio.NewReader(resp.Body)
	for i := 0; i < 5; i++ {
		_, err = reader.ReadString('\n')
		require.Nil(t, err)
	}

	require.Nil(t, resp.Body.Close())

	logs := runTygerSuceeds(t, "run", "logs", runId)
	require.Equal(t, "1\n2\n3\n4\n5", logs)

	// --timestamp should prefix each line with its timestamp
	logs = runTygerSuceeds(t, "run", "logs", runId, "--timestamps")
	lines := strings.Split(logs, "\n")
	require.Equal(t, 5, len(lines))
	var firstTimestamp time.Time
	for i := len(lines) - 1; i >= 0; i-- {
		firstTimestamp, err = time.Parse(time.RFC3339Nano, strings.Split(lines[i], " ")[0])
		require.Nil(t, err)
	}

	// --since one second later. The kubernetes API appears to have a 1-second resolution when evaluating sinceTime
	logs = runTygerSuceeds(t, "run", "logs", runId, "--since", firstTimestamp.Add(time.Second).Format(time.RFC3339Nano))
	require.NotContains(t, logs, "1")

	logs = runTygerSuceeds(t, "run", "logs", runId, "--tail", "3")
	require.Equal(t, "3\n4\n5", logs)
}

func TestGetArchivedLogs(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())

	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "busybox",
		"--command",
		"--",
		"sh", "-c", "echo 1; sleep 1; echo 2; sleep 1; echo 3; sleep 1; echo 4;")

	runId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	t.Logf("Run ID: %s", runId)
	waitForRunStarted(t, runId)
	logs := runTygerSuceeds(t, "run", "logs", runId, "--follow")
	require.Equal(t, "1\n2\n3\n4", logs)

	waitForRunSuccess(t, runId)

	// force logs to be archived
	_, err := controlplane.InvokeRequest(http.MethodPost, "v1/runs/_sweep", nil, nil)
	require.Nil(t, err)

	logs = runTygerSuceeds(t, "run", "logs", runId)
	require.Equal(t, "1\n2\n3\n4", logs)

	// --timestamp should prefix each line with its timestamp
	logs = runTygerSuceeds(t, "run", "logs", runId, "--timestamps")
	lines := strings.Split(logs, "\n")
	require.Equal(t, 4, len(lines))
	var firstTimestamp time.Time
	for i := len(lines) - 1; i >= 0; i-- {
		firstTimestamp, err = time.Parse(time.RFC3339Nano, strings.Split(lines[i], " ")[0])
		require.Nil(t, err)
	}

	// --since
	logs = runTygerSuceeds(t, "run", "logs", runId, "--since", firstTimestamp.Format(time.RFC3339Nano))
	require.Equal(t, "2\n3\n4", logs)
	logs = runTygerSuceeds(t, "run", "logs", runId, "--since", firstTimestamp.Add(time.Minute).Format(time.RFC3339Nano))
	require.Equal(t, "", logs)
	logs = runTygerSuceeds(t, "run", "logs", runId, "--since", firstTimestamp.Add(-time.Minute).Format(time.RFC3339Nano))
	require.Equal(t, "1\n2\n3\n4", logs)

	// --tail
	logs = runTygerSuceeds(t, "run", "logs", runId, "--tail", "3")
	require.Equal(t, "2\n3\n4", logs)
	logs = runTygerSuceeds(t, "run", "logs", runId, "--tail", "0")
	require.Equal(t, "", logs)
	logs = runTygerSuceeds(t, "run", "logs", runId, "--tail", "4")
	require.Equal(t, "1\n2\n3\n4", logs)
}

func TestGetArchivedLogsWithLongLines(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())

	runTygerSuceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "busybox",
		"--command",
		"--",
		"sh", "-c", `head -c 2000000 < /dev/zero | tr '\0' 'a'; echo ""; sleep 1; head -c 2000000 < /dev/zero | tr '\0' 'b';`)

	expectedLogs := strings.Repeat("a", 2000000) + "\n" + strings.Repeat("b", 2000000)

	runId := runTygerSuceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	t.Logf("Run ID: %s", runId)
	waitForRunStarted(t, runId)
	logs := runTygerSuceeds(t, "run", "logs", runId, "--follow")
	require.Equal(t, expectedLogs, logs)

	// force logs to be archived
	_, err := controlplane.InvokeRequest(http.MethodPost, "v1/runs/_sweep", nil, nil)
	require.Nil(t, err)

	logs = runTygerSuceeds(t, "run", "logs", runId)
	require.Equal(t, expectedLogs, logs)
}

func TestConnectivityBetweenJobAndWorkers(t *testing.T) {
	t.Parallel()

	jobCodespecName := strings.ToLower(t.Name()) + "-job"
	workerCodespecName := strings.ToLower(t.Name()) + "-worker"

	digest := runCommandSuceeds(t, "docker", "inspect", "testconnectivity", "--format", "{{ index .RepoDigests 0 }}")

	runTygerSuceeds(t,
		"codespec",
		"create", jobCodespecName,
		"--image", digest,
		"--",
		"--job")

	runTygerSuceeds(t,
		"codespec",
		"create", workerCodespecName,
		"--kind", "worker",
		"--image", digest,
		"--max-replicas", "3",
		"--",
		"--worker")

	runId := runTygerSuceeds(t, "run", "create", "--codespec", jobCodespecName, "--worker-codespec", workerCodespecName, "--worker-replicas", "3", "--timeout", "10m")
	waitForRunSuccess(t, runId)
}

func TestAuthenticationRequired(t *testing.T) {
	t.Parallel()
	ctx, err := controlplane.GetCliContext()
	require.Nil(t, err)
	resp, err := controlplane.NewRetryableClient().Get(fmt.Sprintf("%s/v1/runs/abc", ctx.GetServerUri()))
	require.Nil(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func waitForRunStarted(t *testing.T, runId string) model.Run {
	return waitForRun(t, runId, true)
}

func waitForRunSuccess(t *testing.T, runId string) model.Run {
	return waitForRun(t, runId, false)
}

func waitForRun(t *testing.T, runId string, returnOnRunning bool) model.Run {
	cmd := exec.Command("tyger", "run", "watch", runId, "--full-resource")
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb

	require.NoError(t, cmd.Start(), "unable to start tyger run watch")
	defer cmd.Process.Kill()

	snapshot := model.Run{}
	for {
		line, err := outb.ReadString('\n')
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		require.NoError(t, json.Unmarshal([]byte(line), &snapshot))

		switch snapshot.Status {
		case "Pending":
		case "Running":
			if returnOnRunning {
				return snapshot
			}
		case "Succeeded":
			break
		case "Failed":
			require.FailNowf(t, "run failed.", "Run '%d'. Last status: %s", snapshot.Id, snapshot.Status)
		default:
			require.FailNowf(t, "unexpected run status.", "Run '%d'. Last status: %s", snapshot.Id, snapshot.Status)
		}
	}

	err := cmd.Wait()
	require.NoError(t, err, "tyger run watch failed: %s", errb.String())
	return snapshot
}