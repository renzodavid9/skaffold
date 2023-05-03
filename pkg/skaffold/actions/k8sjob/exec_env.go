/*
Copyright 2023 The Skaffold Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package k8sjob

import (
	"context"
	"fmt"
	"io"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/actions"
	"github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/deploy/label"
	"github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/graph"
	k8sjobutil "github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/k8sjob"
	k8sjoblogger "github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/k8sjob/logger"
	"github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/k8sjob/tracker"
	"github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/kubectl"
	"github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/kubernetes"
	"github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/schema/latest"
	"github.com/GoogleContainerTools/skaffold/v2/pkg/skaffold/util"
)

type ExecEnv struct {
	// Used to print the output from the associated tasks.
	logger *k8sjoblogger.Logger

	// Keeps track of all the jobs created for each associated triggered task.
	tracker *tracker.JobTracker

	// Kubectl client used to manage communication with the cluster.
	kubectl *kubectl.CLI

	// Namespace to use for all kubectl operations.
	namespace string

	// Labeller client.
	labeller *label.DefaultLabeller

	// List of all the local custom actions configurations defined, by name.
	acsCfgByName map[string]latest.Action

	// Global env variables to be injected into every container of each task.
	envVars []corev1.EnvVar
}

var NewExecEnv = newExecEnv

func newExecEnv(ctx context.Context, cfg kubectl.Config, labeller *label.DefaultLabeller /*resources []*latest.PortForwardResource,*/, namespace string, envMap map[string]string, acs []latest.Action) *ExecEnv {
	if namespace == "" {
		namespace = "default"
	}

	kubectl := kubectl.NewCLI(cfg, namespace)

	tracker := tracker.NewContainerTracker()
	logger := k8sjoblogger.NewLogger(ctx, tracker, labeller, kubectl.KubeContext)

	acsCfgByName := map[string]latest.Action{}
	for _, a := range acs {
		acsCfgByName[a.Name] = a
	}

	envVars := []corev1.EnvVar{}
	for k, v := range envMap {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}

	return &ExecEnv{
		kubectl:      kubectl,
		logger:       logger,
		tracker:      tracker,
		namespace:    namespace,
		labeller:     labeller,
		acsCfgByName: acsCfgByName,
		envVars:      envVars,
	}
}

func (e ExecEnv) PrepareActions(ctx context.Context, out io.Writer, allbuilds []graph.Artifact, acsNames []string) ([]actions.Action, error) {
	if err := kubernetes.FailIfClusterIsNotReachable(e.kubectl.KubeContext); err != nil {
		return nil, fmt.Errorf("unable to connect to Kubernetes: %w", err)
	}

	e.logger.Start(ctx, out)

	return e.createActions(ctx, out, allbuilds, acsNames)
}

func (e ExecEnv) Cleanup(ctx context.Context, out io.Writer) error {
	return nil
}

func (e ExecEnv) Stop() {

}

func (e ExecEnv) createActions(ctx context.Context, out io.Writer, bs []graph.Artifact, acsNames []string) ([]actions.Action, error) {
	var acs []actions.Action
	var toTrack []graph.Artifact
	builtArtifacts := map[string]graph.Artifact{}

	for _, b := range bs {
		builtArtifacts[b.ImageName] = b
	}

	for _, aName := range acsNames {
		aCfg, found := e.acsCfgByName[aName]
		if !found {
			return nil, fmt.Errorf("action %v not found for k8s execution mode", aName)
		}

		jmp := aCfg.ExecutionModeConfig.KubernetesClusterExecutionMode.JobManifestPath
		o := aCfg.ExecutionModeConfig.KubernetesClusterExecutionMode.Overrides
		jm, err := e.getJobManifest(jmp, o)
		if err != nil {
			return nil, err
		}

		ts, artifactsToTrack := e.createTasks(ctx, out, aCfg, jm, builtArtifacts)

		acs = append(acs, *actions.NewAction(aCfg.Name, *aCfg.Config.Timeout, *aCfg.Config.IsFailFast, ts))
		toTrack = append(toTrack, artifactsToTrack...)
	}

	e.logger.RegisterArtifacts(toTrack)

	return acs, nil
}

func (e ExecEnv) createTasks(ctx context.Context, out io.Writer, aCfg latest.Action, jobManifest *batchv1.Job, builtArtifacts map[string]graph.Artifact) ([]actions.Task, []graph.Artifact) {
	var ts []actions.Task
	var toTrack []graph.Artifact

	for _, cCfg := range aCfg.Containers {
		art := e.getArtifactToDeploy(builtArtifacts, cCfg)

		ts = append(ts, NewTask(cCfg, e.kubectl, e.namespace, art, *jobManifest, &e))

		toTrack = append(toTrack, graph.Artifact{ImageName: cCfg.Image, Tag: cCfg.Name})
	}

	return ts, toTrack
}

func (e ExecEnv) getArtifactToDeploy(builtArtifacts map[string]graph.Artifact, cfg latest.VerifyContainer) graph.Artifact {
	ba, found := builtArtifacts[cfg.Image]
	artToDeploy := graph.Artifact{ImageName: cfg.Image, Tag: cfg.Image}

	if found {
		artToDeploy.Tag = ba.Tag
	}

	return artToDeploy
}

func (e ExecEnv) getJobManifest(jobManifestPath, overrides string) (*batchv1.Job, error) {
	job, err := e.getBaseJobManifest(jobManifestPath)
	if err != nil {
		return nil, err
	}

	job, err = e.applyOverrides(job, overrides)
	if err != nil {
		return nil, err
	}

	e.setDefaultValues(job)
	return job, nil
}

func (e ExecEnv) getBaseJobManifest(jobManifestPath string) (*batchv1.Job, error) {
	if jobManifestPath == "" {
		return k8sjobutil.GetGenericJob(), nil
	}
	return k8sjobutil.LoadFromPath(jobManifestPath)
}

func (e ExecEnv) applyOverrides(job *batchv1.Job, overrides string) (*batchv1.Job, error) {
	if overrides == "" {
		return job, nil
	}

	obj, err := k8sjobutil.ApplyOverrides(job, overrides)
	if err != nil {
		return nil, err
	}

	return obj.(*batchv1.Job), nil
}

func (e ExecEnv) setDefaultValues(job *batchv1.Job) {
	if job.Labels == nil {
		job.Labels = map[string]string{}
	}

	if job.Spec.Template.Labels == nil {
		job.Spec.Template.Labels = map[string]string{}
	}

	if job.Namespace == "" {
		job.Namespace = e.namespace
	}

	job.Labels["skaffold.dev/run-id"] = e.labeller.GetRunID()
	job.Spec.Template.Labels["skaffold.dev/run-id"] = e.labeller.GetRunID()
	job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever
	job.Spec.BackoffLimit = util.Ptr[int32](0)
}
