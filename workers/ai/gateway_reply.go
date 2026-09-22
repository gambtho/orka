package main

import (
	"context"
	"errors"
	"slices"

	corev1alpha1 "github.com/orka-agents/orka/api/v1alpha1"
	"github.com/orka-agents/orka/internal/aitools"
	"github.com/orka-agents/orka/internal/gateway/workerclient"
	"github.com/orka-agents/orka/internal/labels"
	"github.com/orka-agents/orka/internal/tools"
	"github.com/orka-agents/orka/internal/workerenv"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newNativeGatewayReplySender(
	ctx context.Context, reader client.Reader, env workerenv.AIWorkerEnv, tokenFile string,
) (tools.GatewayReplySender, error) {
	if !env.GatewayReplyEnabled || !slices.Contains(env.Tools, aitools.GatewayReplyToolName) {
		return nil, nil
	}
	unavailable := errors.New("gateway reply requires an authenticated native gateway Task")
	if reader == nil || env.TaskUID == "" || env.TaskNamespace == "" || env.TaskName == "" {
		return nil, unavailable
	}
	task := &corev1alpha1.Task{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: env.TaskNamespace, Name: env.TaskName}, task); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) {
			return nil, unavailable
		}
		// Failure to read optional bootstrap context is not an identity grant,
		// nor a reason to fail otherwise valid model work.
		return nil, nil
	}
	if string(task.UID) != env.TaskUID || task.Spec.Type != corev1alpha1.TaskTypeAI ||
		labels.ParentTaskName(task.Labels, task.Annotations) != "" {
		return nil, unavailable
	}
	sender, err := workerclient.New(workerclient.Config{
		ControllerURL: env.ControllerURL, Namespace: env.TaskNamespace,
		TaskName: env.TaskName, TaskUID: env.TaskUID, TokenFile: tokenFile,
	})
	if err != nil {
		return nil, unavailable
	}
	// Authenticate durable origin, not current message admission. Readiness or
	// capability withdrawal must not hide the tool permanently after startup.
	// Optional service failures deny only this tool; explicit identity rejection
	// still fails startup. Never infer identity from a budget/capability/503 error.
	if err = sender.AuthenticateOrigin(ctx); err != nil {
		if errors.Is(err, workerclient.ErrUnavailable) {
			return nil, nil
		}
		return nil, unavailable
	}
	return sender, nil
}
