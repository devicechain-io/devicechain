// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
)

type executorAccessResult struct {
	Permission string
	Allowed    bool
	Err        error
}

type executorAccessCheck struct {
	Description  string
	Alternatives [][]executorAccessResult
}

// Each check is mandatory; within a check ANY complete permission path suffices.
// Kubernetes permits granting rules already held, even without escalate/bind.
func judgeExecutorAccess(checks []executorAccessCheck) error {
	var failures []string
	for _, check := range checks {
		allowed := false
		var alternatives []string
		for _, path := range check.Alternatives {
			pathAllowed := len(path) > 0
			var missing []string
			for _, result := range path {
				if !result.Allowed || result.Err != nil {
					pathAllowed = false
					permission := result.Permission
					if result.Err != nil {
						permission += " (review failed: " + result.Err.Error() + ")"
					}
					missing = append(missing, permission)
				}
			}
			allowed = allowed || pathAllowed
			if len(missing) > 0 {
				alternatives = append(alternatives, strings.Join(missing, ", "))
			}
		}
		if !allowed {
			failures = append(failures, check.Description+": "+strings.Join(alternatives, " OR "))
		}
	}
	if len(failures) != 0 {
		return fmt.Errorf("executor privilege pre-check refused; this is a pre-check, not a guarantee. Missing permissions:\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
}

type executorReview struct {
	Resource    *authorizationv1.ResourceAttributes
	NonResource *authorizationv1.NonResourceAttributes
}

func (r executorReview) description() string {
	if r.NonResource != nil {
		return r.NonResource.Verb + " " + r.NonResource.Path
	}
	a := r.Resource
	target := a.Resource
	if a.Subresource != "" {
		target += "/" + a.Subresource
	}
	if a.Group != "" {
		target += "." + a.Group
	}
	if a.Name != "" {
		target += "/" + a.Name
	}
	if a.Namespace != "" {
		target += " in namespace " + a.Namespace
	}
	return a.Verb + " " + target
}

type executorAccessRequirement struct {
	Description  string
	Alternatives [][]executorReview
}

func executorResourceReview(verb, group, resource, namespace, name string) executorReview {
	parts := strings.SplitN(resource, "/", 2)
	a := &authorizationv1.ResourceAttributes{Verb: verb, Group: group, Resource: parts[0], Namespace: namespace, Name: name}
	if len(parts) == 2 {
		a.Subresource = parts[1]
	}
	return executorReview{Resource: a}
}

func executorRuleReviews(rules []rbacv1.PolicyRule, namespace string) []executorReview {
	var reviews []executorReview
	for _, rule := range rules {
		names := rule.ResourceNames
		if len(names) == 0 {
			names = []string{""}
		}
		for _, verb := range rule.Verbs {
			for _, group := range rule.APIGroups {
				for _, resource := range rule.Resources {
					for _, name := range names {
						reviews = append(reviews, executorResourceReview(verb, group, resource, namespace, name))
					}
				}
			}
			for _, url := range rule.NonResourceURLs {
				reviews = append(reviews, executorReview{NonResource: &authorizationv1.NonResourceAttributes{Verb: verb, Path: url}})
			}
		}
	}
	return reviews
}

func executorRoleKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

func executorAccessRequirements(objects []unstructured.Unstructured, mapper meta.RESTMapper) ([]executorAccessRequirement, error) {
	var requirements []executorAccessRequirement
	roles := map[string][]rbacv1.PolicyRule{}
	for _, obj := range objects {
		if obj.GetKind() == "Role" || obj.GetKind() == "ClusterRole" {
			var role rbacv1.ClusterRole
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &role); err != nil {
				return nil, fmt.Errorf("reading rules for %s: %w", obj.GetName(), err)
			}
			if len(role.Rules) == 0 {
				return nil, fmt.Errorf("executor role %s unexpectedly grants no rules", obj.GetName())
			}
			roles[executorRoleKey(obj.GetKind(), obj.GetNamespace(), obj.GetName())] = role.Rules
		}
	}
	for _, obj := range objects {
		mapping, err := mapper.RESTMapping(obj.GroupVersionKind().GroupKind(), obj.GroupVersionKind().Version)
		if err != nil {
			return nil, err
		}
		for _, verb := range []string{"create", "patch"} {
			name := obj.GetName()
			if verb == "create" {
				name = "" // Kubernetes create checks are not resource-name scoped.
			}
			review := executorResourceReview(verb, mapping.Resource.Group, mapping.Resource.Resource, obj.GetNamespace(), name)
			requirements = append(requirements, executorAccessRequirement{Description: review.description(), Alternatives: [][]executorReview{{review}}})
		}
		kind := obj.GetKind()
		switch kind {
		case "Role", "ClusterRole":
			direct := executorResourceReview("escalate", "rbac.authorization.k8s.io", mapping.Resource.Resource, obj.GetNamespace(), obj.GetName())
			rules := roles[executorRoleKey(kind, obj.GetNamespace(), obj.GetName())]
			requirements = append(requirements, executorAccessRequirement{
				Description:  "grant rules of " + kind + "/" + obj.GetName(),
				Alternatives: [][]executorReview{{direct}, executorRuleReviews(rules, obj.GetNamespace())},
			})
		case "RoleBinding", "ClusterRoleBinding":
			var binding rbacv1.ClusterRoleBinding
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &binding); err != nil {
				return nil, err
			}
			roleNamespace, resource := obj.GetNamespace(), "roles"
			if binding.RoleRef.Kind == "ClusterRole" {
				roleNamespace, resource = "", "clusterroles"
			}
			rules, ok := roles[executorRoleKey(binding.RoleRef.Kind, roleNamespace, binding.RoleRef.Name)]
			if !ok {
				return nil, fmt.Errorf("executor binding %s references a role absent from the rendered overlay", obj.GetName())
			}
			direct := executorResourceReview("bind", "rbac.authorization.k8s.io", resource, obj.GetNamespace(), binding.RoleRef.Name)
			requirements = append(requirements, executorAccessRequirement{
				Description:  "bind " + binding.RoleRef.Kind + "/" + binding.RoleRef.Name,
				Alternatives: [][]executorReview{{direct}, executorRuleReviews(rules, obj.GetNamespace())},
			})
		}
	}
	for _, review := range []executorReview{
		executorResourceReview("get", "apps", "deployments", executorNamespace, executorName),
		executorResourceReview("get", enrollmentGVR.Group, enrollmentGVR.Resource, "", "cluster"),
	} {
		requirements = append(requirements, executorAccessRequirement{Description: review.description(), Alternatives: [][]executorReview{{review}}})
	}
	return requirements, nil
}

func preflightExecutorAccess(ctx context.Context, typed kubernetes.Interface, objects []unstructured.Unstructured, mapper meta.RESTMapper) error {
	requirements, err := executorAccessRequirements(objects, mapper)
	if err != nil {
		return fmt.Errorf("building executor privilege pre-check: %w", err)
	}
	cache := map[string]executorAccessResult{}
	checks := make([]executorAccessCheck, 0, len(requirements))
	for _, requirement := range requirements {
		check := executorAccessCheck{Description: requirement.Description}
		for _, path := range requirement.Alternatives {
			results := make([]executorAccessResult, 0, len(path))
			for _, review := range path {
				key, err := json.Marshal(review)
				if err != nil {
					return err
				}
				result, ok := cache[string(key)]
				if !ok {
					result.Permission = review.description()
					ssar, err := typed.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
						Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: review.Resource, NonResourceAttributes: review.NonResource},
					}, metav1.CreateOptions{})
					result.Err = err
					if err == nil {
						result.Allowed = ssar.Status.Allowed && !ssar.Status.Denied
						if ssar.Status.EvaluationError != "" {
							result.Err = fmt.Errorf("%s", ssar.Status.EvaluationError)
						}
					}
					cache[string(key)] = result
				}
				results = append(results, result)
			}
			check.Alternatives = append(check.Alternatives, results)
		}
		checks = append(checks, check)
	}
	return judgeExecutorAccess(checks)
}
