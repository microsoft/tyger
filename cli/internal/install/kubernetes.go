// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"bytes"
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	TygerNamespace          = "tyger"
	DefaultTygerReleaseName = TygerNamespace
)

func createTygerNamespace(ctx context.Context, restConfigPromise *Promise[*rest.Config]) (any, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	clientset := kubernetes.NewForConfigOrDie(restConfig)

	_, err = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tyger"}}, metav1.CreateOptions{})
	if err == nil || apierrors.IsAlreadyExists(err) {
		return nil, nil
	}

	return nil, fmt.Errorf("failed to create 'tyger' namespace: %w", err)
}

func createTygerClusterRBAC(ctx context.Context, restConfigPromise *Promise[*rest.Config], createTygerNamespacePromise *Promise[any]) (any, error) {
	config := GetConfigFromContext(ctx)
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	if _, err := createTygerNamespacePromise.Await(); err != nil {
		return nil, errDependencyFailed
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	log.Info().Msgf("Updating RBAC for the '%s' namespace", TygerNamespace)

	role := rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tyger-full-access",
			Namespace: TygerNamespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
		},
	}

	roleBinding := rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tyger-full-access-rolebinding",
			Namespace: TygerNamespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: make([]rbacv1.Subject, 0),
	}

	for _, principal := range config.Cloud.Compute.ManagementPrincipals {
		subject := rbacv1.Subject{
			Name: principal.Id,
		}
		switch principal.Kind {
		case PrincipalKindServicePrincipal:
			subject.Kind = string(PrincipalKindUser)
		default:
			subject.Kind = string(principal.Kind)
		}

		roleBinding.Subjects = append(roleBinding.Subjects, subject)
	}

	if _, err := clientset.RbacV1().Roles(TygerNamespace).Create(ctx, &role, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = clientset.RbacV1().Roles(TygerNamespace).Update(ctx, &role, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to update role: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create role: %w", err)
		}
	}

	if _, err := clientset.RbacV1().RoleBindings(TygerNamespace).Create(ctx, &roleBinding, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = clientset.RbacV1().RoleBindings(TygerNamespace).Update(ctx, &roleBinding, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to update role binding: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create role binding: %w", err)
		}
	}

	clusterRole := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tyger-node-reader",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}

	clusterRoleBinding := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tyger-node-reader-rolebinding",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     clusterRole.Name,
		},
		Subjects: roleBinding.Subjects,
	}

	if _, err := clientset.RbacV1().ClusterRoles().Create(ctx, &clusterRole, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = clientset.RbacV1().ClusterRoles().Update(ctx, &clusterRole, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to update cluster role: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create cluster role: %w", err)
		}
	}

	if _, err := clientset.RbacV1().ClusterRoleBindings().Create(ctx, &clusterRoleBinding, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = clientset.RbacV1().ClusterRoleBindings().Update(ctx, &clusterRoleBinding, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to update cluster role binding: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create cluster role binding: %w", err)
		}
	}

	return nil, nil
}

func PodExec(ctx context.Context, podName string, command ...string) (stdout *bytes.Buffer, stderr *bytes.Buffer, err error) {
	restConfig, err := GetUserRESTConfig(ctx)
	if err != nil {
		return nil, nil, err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	req := clientset.CoreV1().RESTClient().Post().Resource("pods").Name(podName).Namespace(TygerNamespace).SubResource("exec")
	options := &corev1.PodExecOptions{
		Command: command,
		Stdout:  true,
		Stderr:  true,
	}
	req.VersionedParams(options, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(restConfig, "POST", req.URL())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create executor: %w", err)
	}

	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}

	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: stdout,
		Stderr: stderr,
	})
	if err != nil {
		return stdout, stderr, fmt.Errorf("failed to stream remote command: %w", err)
	}

	return stdout, stderr, nil
}
