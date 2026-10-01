package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

type workerRBACDocument struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Rules           []rbacv1.PolicyRule     `json:"rules"`
	AggregationRule *rbacv1.AggregationRule `json:"aggregationRule"`
	RoleRef         rbacv1.RoleRef          `json:"roleRef"`
	Subjects        []rbacv1.Subject        `json:"subjects"`
}

func decodeWorkerRBACDocuments(t *testing.T, manifest string) []workerRBACDocument {
	t.Helper()
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	var documents []workerRBACDocument
	for {
		var document workerRBACDocument
		if err := decoder.Decode(&document); err == io.EOF {
			return documents
		} else if err != nil {
			t.Fatalf("decode RBAC manifest: %v", err)
		}
		if document.Kind != "" {
			documents = append(documents, document)
		}
	}
}

func requireWorkerServiceAccount(t *testing.T, docs []workerRBACDocument, tier, name, namespace string) {
	t.Helper()
	var serviceAccounts []workerRBACDocument
	for _, doc := range docs {
		if doc.Kind == "ServiceAccount" && doc.Metadata.Name == name {
			serviceAccounts = append(serviceAccounts, doc)
		}
	}
	if len(serviceAccounts) != 1 || (serviceAccounts[0].Metadata.Namespace != "" &&
		serviceAccounts[0].Metadata.Namespace != namespace) {
		t.Fatalf("worker %s has %d ServiceAccounts in release namespace %q, want one", tier,
			len(serviceAccounts), namespace)
	}
}

type workerPermission struct {
	group, resource, resourceName, url, verb string
}

// Flatten rules into individual permissions so splitting or reordering rules
// cannot mask a missing grant (or look like a policy change).
func workerPermissions(rules []rbacv1.PolicyRule) map[workerPermission]bool {
	permissions := make(map[workerPermission]bool)
	for _, rule := range rules {
		names := rule.ResourceNames
		if len(names) == 0 {
			names = []string{""} // unrestricted, distinct from a named resource
		}
		for _, verb := range rule.Verbs {
			for _, group := range rule.APIGroups {
				for _, resource := range rule.Resources {
					for _, name := range names {
						permissions[workerPermission{group: group, resource: resource, resourceName: name, verb: verb}] = true
					}
				}
			}
			for _, url := range rule.NonResourceURLs {
				permissions[workerPermission{url: url, verb: verb}] = true
			}
		}
	}
	return permissions
}

func workerRoleUsesExplicitRules(role workerRBACDocument) bool {
	return role.AggregationRule == nil
}

func workerBindingHasSubject(binding workerRBACDocument, name, namespace string) bool {
	for _, subject := range binding.Subjects {
		subjectNamespace := subject.Namespace
		if subjectNamespace == "" && binding.Kind == "RoleBinding" {
			subjectNamespace = binding.Metadata.Namespace
		}
		if subject.Kind == "ServiceAccount" && subject.Name == name && subjectNamespace == namespace {
			return true
		}
	}
	return false
}

func workerBindingHasOnlySubject(binding workerRBACDocument, name, namespace string) bool {
	return len(binding.Subjects) == 1 && workerBindingHasSubject(binding, name, namespace)
}

func TestWorkerRoleRejectsAggregatedPermissions(t *testing.T) {
	const manifest = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ai-worker-role
aggregationRule:
  clusterRoleSelectors:
  - matchLabels:
      orka.ai/worker: ai
rules: []
`
	roles := decodeWorkerRBACDocuments(t, manifest)
	if len(roles) != 1 || workerRoleUsesExplicitRules(roles[0]) {
		t.Fatal("aggregated worker role must not pass explicit-rule parity")
	}
}

func TestWorkerBindingMatchesImplicitSubjectNamespace(t *testing.T) {
	binding := workerRBACDocument{Kind: "RoleBinding"}
	binding.Metadata.Namespace = "orka-test"
	binding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: "test-orka-ai-worker"}}
	if !workerBindingHasOnlySubject(binding, "test-orka-ai-worker", "orka-test") {
		t.Fatal("RoleBinding with one implicit-namespace worker subject must match")
	}
	binding.Subjects = append(binding.Subjects, rbacv1.Subject{Kind: "ServiceAccount", Name: "other-worker"})
	if workerBindingHasOnlySubject(binding, "test-orka-ai-worker", "orka-test") {
		t.Fatal("RoleBinding with an extra subject must not match")
	}
	binding.Subjects = binding.Subjects[:1]
	binding.Metadata.Namespace = "another-namespace"
	if workerBindingHasSubject(binding, "test-orka-ai-worker", "orka-test") {
		t.Fatal("RoleBinding in another namespace must not match")
	}
	binding.Kind = "ClusterRoleBinding"
	binding.Metadata.Namespace = "orka-test"
	if workerBindingHasSubject(binding, "test-orka-ai-worker", "orka-test") {
		t.Fatal("ClusterRoleBinding subject without namespace must not match")
	}
}

func TestStaticChartWorkerRBACMatchesSharedManifest(t *testing.T) {
	shared, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "rbac", "worker_role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := helmTemplateStaticChart(t,
		"--show-only", "templates/rbac.yaml", "--show-only", "templates/serviceaccount.yaml")
	if err != nil {
		t.Fatalf("render static chart RBAC: %v", err)
	}
	sharedDocs := decodeWorkerRBACDocuments(t, string(shared))
	chartDocs := decodeWorkerRBACDocuments(t, rendered)

	const namespace = "orka-test" // helmTemplateStaticChart release namespace
	for _, tier := range []string{"ai", "vendor", "container"} {
		t.Run(tier, func(t *testing.T) {
			checkWorkerRBACParity(t, sharedDocs, chartDocs, tier, namespace)
		})
	}
}

func checkWorkerRBACParity(t *testing.T, sharedDocs, chartDocs []workerRBACDocument, tier, namespace string) {
	t.Helper()
	sharedRoleName := tier + "-worker-role"
	chartRoleName := "test-orka-" + sharedRoleName
	serviceAccountName := "test-orka-" + tier + "-worker"
	requireWorkerServiceAccount(t, chartDocs, tier, serviceAccountName, namespace)
	var sharedRoles, chartRoles []workerRBACDocument
	for _, doc := range sharedDocs {
		if doc.Kind == "ClusterRole" && doc.Metadata.Name == sharedRoleName {
			sharedRoles = append(sharedRoles, doc)
		}
	}
	for _, doc := range chartDocs {
		if doc.Kind == "ClusterRole" && doc.Metadata.Name == chartRoleName {
			chartRoles = append(chartRoles, doc)
		}
	}
	if len(sharedRoles) != 1 || len(chartRoles) != 1 {
		t.Fatalf("%s ClusterRole count: shared=%d chart=%d, want one each", tier, len(sharedRoles), len(chartRoles))
	}
	if !workerRoleUsesExplicitRules(sharedRoles[0]) || !workerRoleUsesExplicitRules(chartRoles[0]) {
		t.Fatalf("%s worker roles must have explicit rules for permission parity", tier)
	}
	sharedGrants := workerPermissions(sharedRoles[0].Rules)
	chartGrants := workerPermissions(chartRoles[0].Rules)
	for _, direction := range []struct {
		name string
		from map[workerPermission]bool
		to   map[workerPermission]bool
	}{
		{"missing from chart", sharedGrants, chartGrants},
		{"extra in chart", chartGrants, sharedGrants},
	} {
		var differences []string
		for permission := range direction.from {
			if !direction.to[permission] {
				differences = append(differences, fmt.Sprintf("%+v", permission))
			}
		}
		slices.Sort(differences)
		if len(differences) > 0 {
			t.Errorf("%s permissions %s:\n%s", tier, direction.name, strings.Join(differences, "\n"))
		}
	}

	var bindings []workerRBACDocument
	for _, doc := range chartDocs {
		workerSubject := workerBindingHasSubject(doc, serviceAccountName, namespace)
		switch doc.Kind {
		case "ClusterRoleBinding":
			if doc.RoleRef.Name == chartRoleName || workerSubject {
				t.Errorf("worker %s has a ClusterRoleBinding %q", tier, doc.Metadata.Name)
			}
		case "RoleBinding":
			if doc.RoleRef.Name == chartRoleName {
				bindings = append(bindings, doc)
			} else if workerSubject {
				t.Errorf("worker %s has an unexpected RoleBinding %q", tier, doc.Metadata.Name)
			}
		}
	}
	if len(bindings) != 1 {
		t.Fatalf("worker %s has %d RoleBindings to its ClusterRole, want one", tier, len(bindings))
	}
	binding := bindings[0]
	if binding.Metadata.Namespace != namespace || binding.RoleRef != (rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: chartRoleName,
	}) || !workerBindingHasOnlySubject(binding, serviceAccountName, namespace) {
		t.Errorf("worker %s RoleBinding %q does not bind only its release-namespace ServiceAccount",
			tier, binding.Metadata.Name)
	}
}
