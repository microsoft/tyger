package install

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	TygerNamespace = "tyger"
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

func createTygerNamespaceRBAC(ctx context.Context, restConfigPromise *Promise[*rest.Config]) (any, error) {
	config := GetConfigFromContext(ctx)

	restConfig, err := restConfigPromise.Await()
	if err != nil {
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

	principals, err := ObjectsIdToPrincipals(ctx, config.Cloud.Compute.ManagementPrincipalIds)
	if err != nil {
		return nil, err
	}

	for _, principal := range principals {
		subject := rbacv1.Subject{
			Name:      principal.Id,
			Namespace: TygerNamespace,
		}
		if principal.Kind == PrincipalKindGroup {
			subject.Kind = "Group"
		} else {
			subject.Kind = "User"
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

	if _, err := clientset.RbacV1().RoleBindings(TygerNamespace).Create(context.TODO(), &roleBinding, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = clientset.RbacV1().RoleBindings(TygerNamespace).Update(context.TODO(), &roleBinding, metav1.UpdateOptions{})
			if err != nil {
				return nil, fmt.Errorf("failed to update role binding: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create role binding: %w", err)
		}
	}

	return nil, nil
}
