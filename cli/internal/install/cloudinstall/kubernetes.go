// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/tyger/cli/internal/install"
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
	DefaultTygerReleaseName = "tyger"
)

func createKubernetesNamespace(ctx context.Context, restConfigPromise *install.Promise[*rest.Config], namespace string) (string, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return namespace, install.ErrDependencyFailed
	}

	clientset := kubernetes.NewForConfigOrDie(restConfig)

	_, err = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{})
	if err == nil || apierrors.IsAlreadyExists(err) {
		return namespace, nil
	}

	return namespace, fmt.Errorf("failed to create '%s' namespace: %w", namespace, err)
}

func deleteKubernetesNamespace(ctx context.Context, restConfig *rest.Config, namespace string) error {
	clientset := kubernetes.NewForConfigOrDie(restConfig)

	err := clientset.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}

	return fmt.Errorf("failed to delete '%s' namespace: %w", namespace, err)
}

func (inst *Installer) createClusterRBAC(ctx context.Context, restConfigPromise *install.Promise[*rest.Config]) (any, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
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
		Subjects: managementPrincipalsToSubjects(inst.Config.Cloud.Compute.ManagementPrincipals),
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

func (inst *Installer) createNamespaceRBAC(ctx context.Context, restConfigPromise *install.Promise[*rest.Config], createNamespacePromise *install.Promise[string]) (any, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	namespace, err := createNamespacePromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	log.Ctx(ctx).Info().Msgf("Updating RBAC for the '%s' namespace", namespace)

	role := rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tyger-full-access",
			Namespace: namespace,
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
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: managementPrincipalsToSubjects(inst.Config.Cloud.Compute.ManagementPrincipals),
	}

	if _, err := clientset.RbacV1().Roles(namespace).Create(ctx, &role, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = clientset.RbacV1().Roles(namespace).Update(ctx, &role, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to update role: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create role: %w", err)
		}
	}

	if _, err := clientset.RbacV1().RoleBindings(namespace).Create(ctx, &roleBinding, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = clientset.RbacV1().RoleBindings(namespace).Update(ctx, &roleBinding, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to update role binding: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create role binding: %w", err)
		}
	}

	return nil, nil
}

func managementPrincipalsToSubjects(principals []Principal) []rbacv1.Subject {
	subjects := make([]rbacv1.Subject, 0, len(principals))
	for _, principal := range principals {
		subject := rbacv1.Subject{
			Name: principal.ObjectId,
		}
		switch principal.Kind {
		case PrincipalKindServicePrincipal:
			subject.Kind = string(PrincipalKindUser)
		case PrincipalKindUser:
			subject.Kind = string(principal.Kind)
			// If this is a guest user, the name should be the object ID,
			// otherwise the name should be the UPN
			if !strings.Contains(principal.UserPrincipalName, "#EXT#@") {
				subject.Name = principal.UserPrincipalName
			}
		default:
			subject.Kind = string(principal.Kind)
		}

		subjects = append(subjects, subject)
	}
	return subjects
}

func (inst *Installer) PodExec(ctx context.Context, namespace string, podName string, command ...string) (stdout *bytes.Buffer, stderr *bytes.Buffer, err error) {
	restConfig, err := inst.GetUserRESTConfig(ctx)
	if err != nil {
		return nil, nil, err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	req := clientset.CoreV1().RESTClient().Post().Resource("pods").Name(podName).Namespace(namespace).SubResource("exec")
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
