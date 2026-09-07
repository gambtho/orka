package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FindSubstrateRecoveryJournal discovers provider cleanup obligations that
// outlive their source RuntimePools. Inspect labels only: even a damaged
// journal requires recovery configuration rather than silently losing cleanup.
func FindSubstrateRecoveryJournal(ctx context.Context, reader client.Reader, namespace string) (string, error) {
	if reader == nil || strings.TrimSpace(namespace) == "" {
		return "", fmt.Errorf("reader and controller namespace are required to discover substrate recovery journals")
	}
	for _, label := range []string{substrateCatalogLabel, substrateTemplateBindingLabel} {
		list := &corev1.ConfigMapList{}
		if err := reader.List(ctx, list, client.InNamespace(strings.TrimSpace(namespace)), client.MatchingLabels{
			label: substrateOwnedLabelValue,
		}, client.Limit(1)); err != nil {
			return "", fmt.Errorf("list substrate recovery journals: %w", err)
		}
		if len(list.Items) > 0 {
			return fmt.Sprintf("recovery ConfigMap %s/%s", list.Items[0].Namespace, list.Items[0].Name), nil
		}
	}
	return "", nil
}
