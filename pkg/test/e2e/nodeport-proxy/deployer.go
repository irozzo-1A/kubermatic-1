package nodeport_proxy

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"k8c.io/kubermatic/v2/pkg/controller/operator/common"
	"k8c.io/kubermatic/v2/pkg/controller/operator/seed/resources/nodeportproxy"
	kubermaticv1 "k8c.io/kubermatic/v2/pkg/crd/kubermatic/v1"
	operatorv1alpha1 "k8c.io/kubermatic/v2/pkg/crd/operator/v1alpha1"
	"k8c.io/kubermatic/v2/pkg/resources/reconciling"
)

const (
	// wait time between poll attempts of a Service vip and/or nodePort.
	// coupled with testTries to produce a net timeout value.
	hitEndpointRetryDelay = 2 * time.Second
	podReadinessTimeout   = 2 * time.Minute
)

type Deployer struct {
	Log       *zap.SugaredLogger
	Namespace string
	Versions  common.Versions
	Client    ctrlclient.Client

	resources []runtime.Object
}

func (d *Deployer) SetUp() error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: d.Namespace,
		},
	}
	if d.Namespace == "" {
		ns.ObjectMeta.GenerateName = "nodeport-proxy-"
	}
	d.Log.Debugw("Creating namespace", "service", ns)
	if err := d.Client.Create(context.TODO(), ns); err != nil {
		return errors.Wrap(err, "failed to create namespace")
	}
	d.Namespace = ns.Name
	d.resources = append(d.resources, ns)

	cfg := &operatorv1alpha1.KubermaticConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubermatic",
			Namespace: d.Namespace,
		},
	}

	recorderFunc := func(create reconciling.ObjectCreator) reconciling.ObjectCreator {
		return func(existing runtime.Object) (runtime.Object, error) {
			obj, err := create(existing)
			if err != nil {
				return nil, err
			}

			d.resources = append(d.resources, obj)
			return existing, nil
		}
	}

	seed, err := common.DefaultSeed(&kubermaticv1.Seed{}, d.Log)
	if err != nil {
		return errors.Wrap(err, "failed to default seed")
	}

	if err := reconciling.ReconcileServiceAccounts(context.TODO(),
		[]reconciling.NamedServiceAccountCreatorGetter{
			nodeportproxy.ServiceAccountCreator(cfg),
		}, d.Namespace, d.Client, recorderFunc); err != nil {
		return errors.Wrap(err, "failed to reconcile ServiceAcconts")
	}
	if err := reconciling.ReconcileRoles(context.TODO(),
		[]reconciling.NamedRoleCreatorGetter{
			nodeportproxy.RoleCreator(),
		}, d.Namespace, d.Client, recorderFunc); err != nil {
		return errors.Wrap(err, "failed to reconcile Role")
	}
	if err := reconciling.ReconcileRoleBindings(context.TODO(),
		[]reconciling.NamedRoleBindingCreatorGetter{
			nodeportproxy.RoleBindingCreator(cfg),
		}, d.Namespace, d.Client, recorderFunc); err != nil {
		return errors.Wrap(err, "failed to reconcile RoleBinding")
	}
	if err := reconciling.ReconcileClusterRoles(context.TODO(),
		[]reconciling.NamedClusterRoleCreatorGetter{
			nodeportproxy.ClusterRoleCreator(cfg),
		}, "", d.Client, recorderFunc); err != nil {
		return errors.Wrap(err, "failed to reconcile ClusterRole")
	}
	if err := reconciling.ReconcileClusterRoleBindings(context.TODO(),
		[]reconciling.NamedClusterRoleBindingCreatorGetter{
			nodeportproxy.ClusterRoleBindingCreator(cfg),
		}, "", d.Client, recorderFunc); err != nil {
		return errors.Wrap(err, "failed to reconcile ClusterRoleBinding")
	}
	if err := reconciling.ReconcileServices(context.TODO(),
		[]reconciling.NamedServiceCreatorGetter{
			nodeportproxy.ServiceCreator(seed)},
		d.Namespace, d.Client, recorderFunc); err != nil {
		return errors.Wrap(err, "failed to reconcile Services")
	}
	if err := reconciling.ReconcileDeployments(context.TODO(),
		[]reconciling.NamedDeploymentCreatorGetter{
			nodeportproxy.EnvoyDeploymentCreator(seed, d.Versions),
			nodeportproxy.UpdaterDeploymentCreator(seed, d.Versions),
		}, d.Namespace, d.Client, recorderFunc); err != nil {
		return errors.Wrap(err, "failed to reconcile Kubermatic Deployments")
	}

	// Wait for pods to be ready
	for _, o := range d.resources {
		if dep, ok := o.(*appsv1.Deployment); ok {
			pods, err := d.waitForPodsCreated(dep)
			if err != nil {
				return errors.Wrap(err, "failed to create pods")
			}
			if err := d.waitForPodsReady(pods...); err != nil {
				return errors.Wrap(err, "failed waiting for pods to be running")
			}
		}
	}
	return nil
}

// CleanUp deletes the resources.
func (d *Deployer) CleanUp() error {
	for _, o := range d.resources {
		// TODO(irozzo) handle better errors
		_ = d.Client.Delete(context.TODO(), o)
	}
	return nil
}

// GetLbService returns the service used to expose the nodeport proxy pods.
func (d *Deployer) GetLbService() *corev1.Service {
	svc := corev1.Service{}
	if err := d.Client.Get(context.TODO(), types.NamespacedName{Name: nodeportproxy.ServiceName, Namespace: d.Namespace}, &svc); err != nil {
		return nil
	}
	return &svc
}

func (d *Deployer) waitForPodsCreated(dep *appsv1.Deployment) ([]string, error) {
	return WaitForPodsCreated(d.Client, int(*dep.Spec.Replicas), dep.Namespace, dep.Spec.Selector.MatchLabels)
}

func (d *Deployer) waitForPodsReady(pods ...string) error {
	if !CheckPodsRunningReady(d.Client, d.Namespace, pods, podReadinessTimeout) {
		return fmt.Errorf("timeout waiting for %d pods to be ready", len(pods))
	}
	return nil
}
