package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/buffers"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/config"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/database"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/model"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/uniqueid"
	"github.com/rs/zerolog/log"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
)

type K8sManager interface {
	CreateRun(ctx context.Context, run model.Run) (*model.Run, error)
	GetRun(ctx context.Context, id string) (*model.Run, error)
	HealthCheck(ctx context.Context) error
}

type manager struct {
	config        config.ConfigSpec
	clientset     *kubernetes.Clientset
	repository    database.Repository
	bufferManager buffers.BufferManager
}

func NewK8sManager(config config.ConfigSpec, repository database.Repository, bufferManager buffers.BufferManager) (K8sManager, error) {
	var k8sConfig *rest.Config
	var err error

	if config.KubeconfigPath == "" {
		k8sConfig, err = rest.InClusterConfig()
	} else {
		k8sConfig, err = clientcmd.BuildConfigFromFlags("", config.KubeconfigPath)
	}

	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, err
	}

	return manager{config, clientset, repository, bufferManager}, nil
}

func (m manager) CreateRun(ctx context.Context, run model.Run) (*model.Run, error) {

	codespec, normalizedCodespecRef, err := m.getCodespec(ctx, run.CodeSpec)
	if err != nil {
		return nil, err
	}

	id := uniqueid.NewId()
	k8sId := fmt.Sprintf("run-%s", id)

	run.Id = id
	run.CodeSpec = *normalizedCodespecRef

	annotationBytes, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}

	bufferMap, err := m.getBufferMap(ctx, codespec.Buffers, run.Buffers)
	if err != nil {
		return nil, err
	}

	secret := v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: k8sId,
			Labels: map[string]string{
				"tyger": "run",
			},
		},
		StringData: bufferMap,
	}

	_, err = m.clientset.CoreV1().Secrets(m.config.KubernetesNamespace).Create(ctx, &secret, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	log.Ctx(ctx).Info().Msgf("Created secret %s", k8sId)

	env := make([]v1.EnvVar, 0)
	for k, v := range codespec.Env {
		env = append(env, v1.EnvVar{Name: k, Value: v})
	}

	for k := range bufferMap {
		env = append(
			env, v1.EnvVar{
				Name:  fmt.Sprintf("%s_BUFFER_URI_FILE", strings.ToUpper(k)),
				Value: fmt.Sprintf("/etc/buffer-sas-tokens/%s", k)})
	}

	env = append(env, v1.EnvVar{Name: "MRD_STORAGE_URI", Value: m.config.MrdStorageUri})

	container := v1.Container{
		Name:    "runner",
		Image:   codespec.Image,
		Command: codespec.Command,
		Args:    codespec.Args,
		Env:     env,
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      "buffers",
				MountPath: "/etc/buffer-sas-tokens",
				ReadOnly:  true,
			},
		},
	}

	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: k8sId,
			Annotations: map[string]string{
				"run": string(annotationBytes),
			},
			Labels: map[string]string{
				"tyger": "run",
			},
		},
		Spec: v1.PodSpec{
			Containers:    []v1.Container{container},
			RestartPolicy: v1.RestartPolicyOnFailure,
			Volumes: []v1.Volume{
				{
					Name: "buffers",
					VolumeSource: v1.VolumeSource{
						Secret: &v1.SecretVolumeSource{SecretName: k8sId},
					},
				},
			},
		},
	}

	_, err = m.clientset.CoreV1().Pods(m.config.KubernetesNamespace).Create(ctx, &pod, metav1.CreateOptions{})
	if err != nil {
		return &run, err
	}

	log.Ctx(ctx).Info().Msgf("Created run %s", k8sId)

	run.Id = id
	return &run, nil
}

func (m manager) GetRun(ctx context.Context, id string) (*model.Run, error) {
	pod, err := m.clientset.CoreV1().Pods(m.config.KubernetesNamespace).Get(ctx, fmt.Sprintf("run-%s", id), metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, model.ErrNotFound
		}

		return nil, fmt.Errorf("get pod: %v", err)
	}

	run := model.Run{}

	if err := json.Unmarshal([]byte(pod.Annotations["run"]), &run); err != nil {
		return nil, fmt.Errorf("read annotation: %v", err)
	}

	run.Status = getPodStatus(ctx, pod)
	return &run, nil
}

func getPodStatus(ctx context.Context, pod *v1.Pod) string {
	if pod.Status.Phase == "Pending" {
		return "Pending"
	}

	state := pod.Status.ContainerStatuses[0].State
	if state.Waiting != nil {
		return state.Waiting.Reason
	}
	if state.Running != nil {
		return "Running"
	}
	if state.Terminated != nil {
		return state.Terminated.Reason
	}

	log.Ctx(ctx).Err(fmt.Errorf("unknown pod container state"))
	return string(pod.Status.Phase)
}

func (m manager) getBufferMap(ctx context.Context, parameters *model.BufferParameters, arguments map[string]string) (map[string]string, error) {

	argumentsCopy := make(map[string]string, len(arguments))
	for k, v := range arguments {
		argumentsCopy[k] = v
	}

	type parameter struct {
		Name      string
		Writeable bool
	}

	combinedParameters := make([]parameter, 0)
	if parameters != nil {
		for _, v := range parameters.Inputs {
			combinedParameters = append(combinedParameters, parameter{v, false})
		}
		for _, v := range parameters.Outputs {
			combinedParameters = append(combinedParameters, parameter{v, true})
		}
	}

	outputMap := make(map[string]string)
	for _, p := range combinedParameters {
		buffer, ok := argumentsCopy[p.Name]
		if !ok {
			return nil, &model.ValidationError{Message: fmt.Sprintf("Run is missing required buffer argument '%s'", p.Name)}
		}

		uri, err := m.bufferManager.GetSasUri(ctx, buffer, p.Writeable, false /* externalAccess */)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return nil, &model.ValidationError{Message: fmt.Sprintf("The buffer '%s' was not found", buffer)}
			}
			return nil, err
		}

		outputMap[p.Name] = uri
		delete(argumentsCopy, p.Name)
	}

	for k := range argumentsCopy {
		return nil, &model.ValidationError{Message: fmt.Sprintf("Buffer argument '%s' does not correspond to a buffer parameter on the codespec", k)}
	}

	return outputMap, nil
}

func (m manager) getCodespec(ctx context.Context, codespecRef string) (spec *model.Codespec, normalizedRef *string, err error) {
	codespecTokens := strings.Split(codespecRef, "/versions/")
	var codespec *model.Codespec
	var version *int

	switch len(codespecTokens) {
	case 1:
		codespec, version, err = m.repository.GetLatestCodespec(ctx, codespecTokens[0])
	case 2:
		*version, err = strconv.Atoi(codespecTokens[1])
		if err != nil {
			err = database.ErrNotFound
		} else {
			codespec, err = m.repository.GetCodespecVersion(ctx, codespecTokens[0], *version)
		}
	default:
		err = database.ErrNotFound
	}

	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return nil, nil, &model.ValidationError{
				Message: fmt.Sprintf(
					"The codespec '%s' was not found. The value should be in the form '<codespec_name>' or '<codespec_name>/versions/<version_number>'.",
					codespecRef)}
		}
		return nil, nil, err
	}

	normalizedRef = pointer.String(fmt.Sprintf("%s/versions/%d", codespecTokens[0], *version))

	return codespec, normalizedRef, nil
}

func (m manager) HealthCheck(ctx context.Context) error {
	path := "/livez"
	res := m.clientset.Discovery().RESTClient().Get().AbsPath(path).Do(ctx)
	err := res.Error()
	if err != nil {
		log.Ctx(ctx).Err(err).Msg("kubernetes health check failed")
		return errors.New("kubernetes health check failed")
	}

	return nil
}
