/*
Copyright (c) 2026.

MIT License - see LICENSE file for details.
*/

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
)

func TestJobBuilderAzureDeploymentEnv(t *testing.T) {
	for _, deployment := range []string{"primary-slot", ""} {
		t.Run("deployment="+deployment, func(t *testing.T) {
			primary := &corev1alpha1.Provider{Spec: corev1alpha1.ProviderSpec{
				Type: corev1alpha1.ProviderTypeAzureOpenAI, DefaultModel: "provider-model",
				Azure: &corev1alpha1.AzureConfig{DeploymentName: deployment, APIVersion: "2024-06-01"},
			}}
			fallback := &corev1alpha1.Provider{ObjectMeta: metav1.ObjectMeta{Name: "fallback", Namespace: "default"}, Spec: corev1alpha1.ProviderSpec{
				Type:  corev1alpha1.ProviderTypeAzureOpenAI,
				Azure: &corev1alpha1.AzureConfig{DeploymentName: "fallback-slot", APIVersion: "2024-10-21"},
			}}
			builder := setupJobBuilder()
			builder.Client = fake.NewClientBuilder().WithScheme(builder.Scheme()).WithObjects(fallback).Build()
			agent := &corev1alpha1.Agent{Spec: corev1alpha1.AgentSpec{Model: &corev1alpha1.ModelConfig{
				Name: "agent-model", Fallbacks: []corev1alpha1.ModelFallback{{ProviderRef: "fallback", Model: "fallback-model"}},
			}}}
			task := &corev1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "task", Namespace: "default"}, Spec: corev1alpha1.TaskSpec{
				Type: corev1alpha1.TaskTypeAI, AI: &corev1alpha1.AISpec{Model: "task-model", Prompt: "hello"},
			}}
			envVars, err := builder.addAIEnvVars(t.Context(), nil, task, agent, primary, JobBuildOptions{})
			require.NoError(t, err)
			values := map[string]string{}
			for _, envVar := range envVars {
				values[envVar.Name] = envVar.Value
			}
			assert.Equal(t, "task-model", values["ORKA_AI_MODEL"])
			assert.Equal(t, "fallback-model", values["ORKA_AI_FALLBACK_0_MODEL"])
			assert.Equal(t, deployment, values["ORKA_AI_AZURE_DEPLOYMENT_NAME"])
			assert.Equal(t, "fallback-slot", values["ORKA_AI_FALLBACK_0_AZURE_DEPLOYMENT_NAME"])
			assert.Equal(t, "2024-06-01", values["ORKA_AI_AZURE_API_VERSION"])
			assert.Equal(t, "2024-10-21", values["ORKA_AI_FALLBACK_0_AZURE_API_VERSION"])
			if deployment == "" {
				assert.NotContains(t, values, "ORKA_AI_AZURE_DEPLOYMENT_NAME")
			}
		})
	}
}
