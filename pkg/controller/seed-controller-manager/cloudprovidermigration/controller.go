/*
Copyright 2020 The Kubermatic Kubernetes Platform contributors.

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

package cloudprovidermigration

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"go.uber.org/zap"

	kubermaticv1 "k8c.io/kubermatic/v2/pkg/crd/kubermatic/v1"
	kubermaticv1helper "k8c.io/kubermatic/v2/pkg/crd/kubermatic/v1/helper"
	"k8c.io/kubermatic/v2/pkg/resources"
	"k8s.io/apimachinery/pkg/util/wait"
	//"k8c.io/kubermatic/v2/pkg/resources/apiserver"
	"k8c.io/kubermatic/v2/pkg/resources/cloudcontroller"
	"k8c.io/kubermatic/v2/pkg/resources/controllermanager"
	"k8c.io/kubermatic/v2/pkg/resources/machinecontroller"
	reconciling "k8c.io/kubermatic/v2/pkg/resources/reconciling"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	// refactor to remove this dependency
	"k8c.io/kubermatic/v2/pkg/controller/seed-controller-manager/kubernetes"
	"k8c.io/kubermatic/v2/pkg/provider"

	kubeapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	ControllerName = "kubermatic_ccm_migration_controller"
)

// Reconciler is a controller which is responsible for managing clusters
type Reconciler struct {
	ctrlruntimeclient.Client
	kubernetes.ClusterReconcilerConfig
	log        *zap.SugaredLogger
	workerName string
	seedGetter provider.SeedGetter
}

// NewController creates a cluster controller.
func Add(
	mgr manager.Manager,
	log *zap.SugaredLogger,
	numWorkers int,
	seedGetter provider.SeedGetter,
	config kubernetes.ClusterReconcilerConfig) error {

	reconciler := &Reconciler{
		log:                     log.Named(ControllerName),
		seedGetter:              seedGetter,
		Client:                  mgr.GetClient(),
		ClusterReconcilerConfig: config,
	}

	c, err := controller.New(ControllerName, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: numWorkers})
	if err != nil {
		return err
	}

	return c.Watch(&source.Kind{Type: &kubermaticv1.Cluster{}}, &handler.EnqueueRequestForObject{}, ccmMigrationAnnotationPredicate{})
}

func (r *Reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := r.log.With("request", request)
	log.Debug("Processing")

	cluster := &kubermaticv1.Cluster{}
	if err := r.Get(ctx, request.NamespacedName, cluster); err != nil {
		if kubeapierrors.IsNotFound(err) {
			log.Debug("Could not find cluster")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	seed, err := r.seedGetter()
	if err != nil {
		return reconcile.Result{}, err
	}

	log = log.With("cluster", cluster.Name)

	td, err := r.GetClusterTemplateData(ctx, r.Client, cluster, seed)
	if err != nil {
		return reconcile.Result{}, err
	}
	//kasDeploymentName, kasCreator := apiserver.DeploymentCreator(td, r.Features.KubernetesOIDCAuthentication)()
	cmDeploymentName, cmCreator := controllermanager.DeploymentCreator(td, "*", "tokencleaner", "-cloud-node-lifecycle", "-route", "-service")()
	ccmDeploymentName, ccmCreator := cloudcontroller.DeploymentCreator(td)()
	mcDeploymentName, mcCreator := machinecontroller.DeploymentCreator(td)()
	ccmKubeconfigSecreteName, ccmKubeconfigCreator := resources.GetInternalKubeconfigCreator(resources.CloudControllerManagerKubeconfigSecretName, resources.CloudControllerManagerCertUsername, nil, td)()
	// Define resource graph
	if graph, err := reconciling.NewTasksGraphBuilder().
		AddTask("apply-ccm-kubeconfig", reconciling.ReconcileSecretTaskFn(ccmKubeconfigCreator, ccmKubeconfigSecreteName, cluster.Status.NamespaceName)).
		// Deactivate cloud-controllers in KCM deployment.
		AddTask("deactivate-cloud-controllers",
			reconciling.ReconcileDeploymentTaskFn(cmCreator, cmDeploymentName, cluster.Status.NamespaceName)).
		// Deploy the MC to set provider to external.
		AddTask("update-kubelet-parameters",
			reconciling.ReconcileDeploymentTaskFn(mcCreator, mcDeploymentName, cluster.Status.NamespaceName)).
		// Wait for KCM to rollout.
		AddTask("wait-for-cloud-controllers-deactivated",
			waitForDeploymentComplete(cmDeploymentName, cluster.Status.NamespaceName, 10*time.Second),
			"deactivate-cloud-controllers").
		// Wait for KCM to rollout.
		AddTask("wait-for-mc-ready",
			waitForDeploymentComplete(mcDeploymentName, cluster.Status.NamespaceName, 10*time.Second),
			"update-kubelet-parameters").
		// Deploy the CCM with cloud-node controller deactivated because not
		// kubelet flags have not been deactivated yet in machine-controller.
		AddTask("deploy-ccm",
			reconciling.ReconcileDeploymentTaskFn(ccmCreator, ccmDeploymentName, cluster.Status.NamespaceName),
			"apply-ccm-kubeconfig",
			"wait-for-cloud-controllers-deactivated",
			"wait-for-mc-ready").
		AddTask("wait-for-ccm-ready",
			waitForDeploymentComplete(ccmDeploymentName, cluster.Status.NamespaceName, 10*time.Second),
			"deploy-ccm").
		AddTask("update-cluster",
			func(ctx context.Context, cli ctrlruntimeclient.Client) error {
				return updateCluster(ctx, cli, cluster, r.success)
			},
			"wait-for-ccm-ready",
		).Build(); err == nil { // TODO(iacopo): Add BuildOrDie() there is nothing we can do at runtime in case the described graph has loops or miss some dependencies apart from panic-ing.
		log.Debugw("Running task graph", "graph", graph)
		if s := reconciling.RunTasks(ctx, r.Client, graph, &LoggingTaskEventHandler{Log: r.log}); len(s.Failed()) > 0 {
			// TODO(iacopo) it would be possible to extend the task library to
			// have tasks that are run on failure of other tasks, to avoid the
			// following.
			updateCluster(ctx, r.Client, cluster, r.failure)
			// retry if some of the tasks failed.
			return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
		} else {
			log.Debugw("some tasks failed", "result", s)
		}
	} else {
		panic(err)
	}

	return reconcile.Result{}, nil
}

var _ reconciling.TaskEventHandler = &LoggingTaskEventHandler{}

type LoggingTaskEventHandler struct {
	Log *zap.SugaredLogger
}

func (l *LoggingTaskEventHandler) OnStarted(id reconciling.TaskID) {
	l.Log.Debugw("task started", "ID", id)
}

func (l *LoggingTaskEventHandler) OnSuccess(id reconciling.TaskID) {
	l.Log.Debugw("task succeeded", "ID", id)
}

func (l *LoggingTaskEventHandler) OnSkipped(id reconciling.TaskID) {
	l.Log.Debugw("task skipped", "ID", id)
}

func (l *LoggingTaskEventHandler) OnError(id reconciling.TaskID, err error) {
	l.Log.Errorw("error occurred on task", "ID", id, "error", err)
}

func (r *Reconciler) success(cluster *kubermaticv1.Cluster) {
	//delete(cluster.Annotations, kubernetes.CCMMigrationNeededAnnotation)
	kubermaticv1helper.SetClusterCondition(cluster, r.Versions, kubermaticv1.ClusterConditionCCMMigrationCompleted, corev1.ConditionTrue, kubermaticv1.ReasonClusterCCMMigrationSuccesfull, "CCM migration completed successfully")
}

func (r *Reconciler) failure(cluster *kubermaticv1.Cluster) {
	kubermaticv1helper.SetClusterCondition(cluster, r.Versions, kubermaticv1.ClusterConditionCCMMigrationCompleted, corev1.ConditionFalse, kubermaticv1.ReasonClusterCCMMigrationInProgress, "CCM migration did not complete")
}

func updateCluster(ctx context.Context, client ctrlruntimeclient.Client, cluster *kubermaticv1.Cluster, updateFn func(*kubermaticv1.Cluster)) error {
	oldCluster := cluster.DeepCopy()
	updateFn(cluster)
	if reflect.DeepEqual(oldCluster, cluster) {
		return nil
	}
	return client.Patch(ctx, cluster, ctrlruntimeclient.MergeFrom(oldCluster))
}

func deactivateCloudContollersModifier(rawcreate reconciling.ObjectCreator) reconciling.ObjectCreator {
	return containerFlagModifier(rawcreate, resources.ControllerManagerDeploymentName, "--controllers", "*,bootstrapsigner,tokencleaner,-cloud-node-lifecycle,-route,-service")
}

func deactivateCloudNodeControllerModifier(rawcreate reconciling.ObjectCreator) reconciling.ObjectCreator {
	return containerFlagModifier(rawcreate, cloudcontroller.OpenStackCCMName, "--controllers", "*,-cloud-node")
}

func containerFlagModifier(rawcreate reconciling.ObjectCreator, containerName, flagName, flagValue string) reconciling.ObjectCreator {
	return func(existing runtime.Object) (runtime.Object, error) {
		obj, err := rawcreate(existing)
		if err != nil {
			return nil, err
		}
		if d, ok := obj.(*appsv1.Deployment); ok {
			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name == containerName {
					for i, arg := range c.Args {
						if arg == flagName {
							if i+1 < len(c.Args) {
								c.Args[i+1] = flagValue
							} else {
								c.Args = append(c.Args, flagValue)
							}
							return d, nil
						}
					}
					c.Args = append(c.Args, flagName, flagValue)
					return d, nil
				}
			}
			return nil, fmt.Errorf("container %s not found", containerName)
		}
		return nil, fmt.Errorf("unsupported kind: %s", obj.GetObjectKind().GroupVersionKind().Kind)
	}
}

func waitForDeploymentComplete(name, namespace string, maxWait time.Duration) reconciling.TaskFn {
	return func(ctx context.Context, client ctrlruntimeclient.Client) error {
		wait.PollImmediate(1*time.Second, maxWait, func() (bool, error) {
			deployment := appsv1.Deployment{}
			if err := client.Get(ctx, ctrlruntimeclient.ObjectKey{Name: name, Namespace: namespace}, &deployment); err != nil {
				//TODO(irozzo): swallowing error at the moment, handle this
				//better.
				return false, nil
			}
			return isDeploymentReady(&deployment)
		})
		return nil
	}
}

// based on:
// https://github.com/kubernetes/kubernetes/blob/252887e39f905389156d2bc9c5932688857588e4/staging/src/k8s.io/kubectl/pkg/polymorphichelpers/rollout_status.go#L59
func isDeploymentReady(deployment *appsv1.Deployment) (bool, error) {
	if deployment.Generation <= deployment.Status.ObservedGeneration {
		cond := GetDeploymentCondition(deployment.Status, appsv1.DeploymentProgressing)
		if cond != nil && cond.Reason == "ProgressDeadlineExceeded" {
			return false, fmt.Errorf("deployment %q exceeded its progress deadline", deployment.Name)
		}
		if deployment.Spec.Replicas != nil && deployment.Status.UpdatedReplicas < *deployment.Spec.Replicas {
			return false, nil
		}
		if deployment.Status.Replicas > deployment.Status.UpdatedReplicas {
			return false, nil
		}
		if deployment.Status.AvailableReplicas < deployment.Status.UpdatedReplicas {
			return false, nil
		}
		return true, nil
	}
	return false, nil
}

// GetDeploymentCondition returns the condition with the provided type.
func GetDeploymentCondition(status appsv1.DeploymentStatus, condType appsv1.DeploymentConditionType) *appsv1.DeploymentCondition {
	for i := range status.Conditions {
		c := status.Conditions[i]
		if c.Type == condType {
			return &c
		}
	}
	return nil
}

// ccmMigrationAnnotationPredicate is used to filter out events associated to
// clusters that do not have the migration annotation.
type ccmMigrationAnnotationPredicate struct{}

// Create returns true if the Create event should be processed
func (e ccmMigrationAnnotationPredicate) Create(event event.CreateEvent) bool {
	return e.match(event.Meta)
}

// Delete returns true if the Delete event should be processed
func (e ccmMigrationAnnotationPredicate) Delete(event event.DeleteEvent) bool {
	return e.match(event.Meta)
}

// Update returns true if the Update event should be processed
func (e ccmMigrationAnnotationPredicate) Update(event event.UpdateEvent) bool {
	return e.match(event.MetaNew)
}

// Generic returns true if the Generic event should be processed
func (e ccmMigrationAnnotationPredicate) Generic(event event.GenericEvent) bool {
	return e.match(event.Meta)
}

func (e ccmMigrationAnnotationPredicate) match(obj metav1.Object) bool {
	_, ok := obj.GetAnnotations()[kubernetes.CCMMigrationNeededAnnotation]
	return ok
}
