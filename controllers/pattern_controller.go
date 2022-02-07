/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"path/filepath"

	"github.com/go-errors/errors"
	"github.com/go-logr/logr"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	klog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configclient "github.com/openshift/client-go/config/clientset/versioned"

	//	olmapi "github.com/operator-framework/api/pkg/operators/v1alpha1"

	api "github.com/hybrid-cloud-patterns/patterns-operator/api/v1alpha1"
)

// PatternReconciler reconciles a Pattern object
type PatternReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	logger logr.Logger

	config       *rest.Config
	configClient configclient.Interface
}

//+kubebuilder:rbac:groups=gitops.hybrid-cloud-patterns.io,resources=patterns,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gitops.hybrid-cloud-patterns.io,resources=patterns/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=gitops.hybrid-cloud-patterns.io,resources=patterns/finalizers,verbs=update
//+kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=list;get
//+kubebuilder:rbac:groups=config.openshift.io,resources=ingresses,verbs=list;get
//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=list;get;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=namespaces;secrets;configmaps,verbs=list;get;create;update;patch;delete
//+kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=list;get;create;update;patch;delete
//+kubebuilder:rbac:groups=operators.coreos.com,resources=subscriptions,verbs=list;get;create;update;patch;delete
//

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// The Reconcile function compares the state specified by
// the Pattern object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *PatternReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Reconcile() should perform at most one action in any invocation
	// in order to simplify testing.
	r.logger = klog.FromContext(ctx)
	r.logger.Info("Reconciling Pattern")

	// Logger includes name and namespace
	// Its also wants arguments in pairs, eg.
	// r.logger.Error(err, fmt.Sprintf("[%s/%s] %s", p.Name, p.ObjectMeta.Namespace, reason))
	// Or r.logger.Error(err, "message", "name", p.Name, "namespace", p.ObjectMeta.Namespace, "reason", reason))

	// Fetch the NodeMaintenance instance
	instance := &api.Pattern{}
	err := r.Client.Get(context.TODO(), req.NamespacedName, instance)
	if err != nil {
		if kerrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			r.logger.Info("Pattern not found")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		r.logger.Info("Error reading the request object, requeuing.")
		return reconcile.Result{}, err
	}

	// Remove the ArgoCD application on deletion
	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		// Add finalizer when object is created
		if !ContainsString(instance.ObjectMeta.Finalizers, api.PatternFinalizer) {
			instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers, api.PatternFinalizer)
			err := r.Client.Update(context.TODO(), instance)
			return r.actionPerformed(instance, "updated finalizer", err)
		}

	} else if err := r.finalizeObject(instance); err != nil {
		return reconcile.Result{}, err

	} else {
		return reconcile.Result{}, nil
	}

	// -- Fill in defaults (changes made to a copy and not persisted)
	err, qualifiedInstance := r.applyDefaults(instance)
	if err != nil {
		return r.actionPerformed(qualifiedInstance, "applying defaults", err)
	}

	if err := r.preValidation(qualifiedInstance); err != nil {
		return r.actionPerformed(qualifiedInstance, "prerequisite validation", err)
	}

	// -- GitOps Subscription
	targetSub := newSubscription(*qualifiedInstance)
	controllerutil.SetOwnerReference(qualifiedInstance, targetSub, r.Scheme)

	err, sub := getSubscription(r.config, targetSub.Name, targetSub.Namespace)
	if sub == nil {
		err := createSubscription(r.config, targetSub)
		return r.actionPerformed(qualifiedInstance, "create gitops subscription", err)

	} else if ownedBySame(targetSub, sub) {
		// Check version/channel etc
		err, changed := updateSubscription(r.config, targetSub, sub)
		if changed {
			return r.actionPerformed(qualifiedInstance, "update gitops subscription", err)
		}

	} else {
		logOnce("The gitops subscription is not owned by us, leaving untouched")
	}

	logOnce("subscription found")

	// -- GitOps Namespace (created by the gitops operator)
	if haveNamespace(r.config, applicationNamespace) == false {
		return r.actionPerformed(qualifiedInstance, "check application namespace", fmt.Errorf("waiting for creation"))
	}

	logOnce("namespace found")

	// -- ArgoCD Application
	targetApp := newApplication(*qualifiedInstance)
	controllerutil.SetOwnerReference(qualifiedInstance, targetApp, r.Scheme)

	log.Printf("Targeting: %s\n", objectYaml(targetApp))

	err, app := getApplication(r.config, qualifiedInstance.Name)
	if app == nil {
		log.Printf("App not found: %s\n", err.Error())
		err := createApplication(r.config, targetApp)
		return r.actionPerformed(qualifiedInstance, "create application", err)

	} else if ownedBySame(targetApp, app) {
		// Check values
		err, changed := updateApplication(r.config, targetApp, app)
		if changed {
			if err != nil {
				qualifiedInstance.Status.Version = 1 + qualifiedInstance.Status.Version
			}
			return r.actionPerformed(qualifiedInstance, "updated application", err)
		}

	} else {
		// Someone manually removed the owner ref
		return r.actionPerformed(qualifiedInstance, "create application", fmt.Errorf("We no longer own Application %q", targetApp.Name))
	}

	// Perform validation of the site values file(s)
	if err := r.postValidation(qualifiedInstance); err != nil {
		return r.actionPerformed(qualifiedInstance, "validation", err)
	}
	// Report statistics

	minutes := time.Duration(qualifiedInstance.Spec.ReconcileMinutes)
	log.Printf("\n\x1b[32;1m\tReconcile complete - waiting %d minutes\x1b[0m\n", minutes)

	return ctrl.Result{RequeueAfter: time.Minute * minutes}, nil
}

func (r *PatternReconciler) preValidation(input *api.Pattern) error {

	//ss := strings.Compare(input.Spec.GitConfig.TargetRepo, "git")
	// TARGET_REPO=$(shell git remote show origin | grep Push | sed -e 's/.*URL:[[:space:]]*//' -e 's%:[a-z].*@%@%' -e 's%:%/%' -e 's%git@%https://%' )
	if index := strings.Index(input.Spec.GitConfig.TargetRepo, "git@"); index == 0 {
		return errors.New(fmt.Errorf("Invalid TargetRepo: %s", input.Spec.GitConfig.TargetRepo))
	}

	// Check the url is reachable

	return nil
}

func (r *PatternReconciler) postValidation(input *api.Pattern) error {
	return nil
}

func (r *PatternReconciler) applyDefaults(input *api.Pattern) (error, *api.Pattern) {

	output := input.DeepCopy()
	if output.Spec.ReconcileMinutes == 0 {
		output.Spec.ReconcileMinutes = 10
	}

	// Cluster ID:
	// oc get clusterversion -o jsonpath='{.items[].spec.clusterID}{"\n"}'
	// oc get clusterversion/version -o jsonpath='{.spec.clusterID}'
	if cv, err := r.configClient.ConfigV1().ClusterVersions().Get(context.Background(), "version", metav1.GetOptions{}); err != nil {
		return err, output
	} else {
		output.Status.ClusterID = string(cv.Spec.ClusterID)
	}

	// Derive cluster and domain names
	// oc get Ingress.config.openshift.io/cluster -o jsonpath='{.spec.domain}'
	clusterIngress, err := r.configClient.ConfigV1().Ingresses().Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		return err, output
	}

	// "apps.mycluster.blueprints.rhecoeng.com"
	output.Status.ClusterDomain = clusterIngress.Spec.Domain

	if len(output.Spec.GitConfig.TargetRevision) == 0 {
		output.Spec.GitConfig.TargetRevision = "main"
	}

	// Set output.Spec.GitConfig.ValuesDirectoryURL based on the TargetRepo
	if len(output.Spec.GitConfig.ValuesDirectoryURL) == 0 && output.Spec.GitConfig.Hostname == "github.com" {
		// https://github.com/hybrid-cloud-patterns/industrial-edge/raw/main/
		ss := fmt.Sprintf("%s/raw/%s/", output.Spec.GitConfig.TargetRepo, output.Spec.GitConfig.TargetRevision)
		output.Spec.GitConfig.ValuesDirectoryURL = strings.ReplaceAll(ss, ".git", "")
	}

	if len(output.Spec.GitConfig.Hostname) == 0 {
		ss := strings.Split(output.Spec.GitConfig.TargetRepo, "/")
		output.Spec.GitConfig.Hostname = ss[2]
	}

	if output.Spec.GitOpsConfig == nil {
		output.Spec.GitOpsConfig = &api.GitOpsConfig{}
	}
	if len(output.Spec.GitOpsConfig.SyncPolicy) == 0 {
		output.Spec.GitOpsConfig.SyncPolicy = api.InstallAutomatic
	}

	if len(output.Spec.GitOpsConfig.InstallPlanApproval) == 0 {
		output.Spec.GitOpsConfig.InstallPlanApproval = api.InstallAutomatic
	}

	if len(output.Spec.GitOpsConfig.OperatorChannel) == 0 {
		output.Spec.GitOpsConfig.OperatorChannel = "stable"
	}

	if len(output.Spec.GitOpsConfig.OperatorSource) == 0 {
		output.Spec.GitOpsConfig.OperatorSource = "redhat-operators"
	}
	if len(output.Spec.GitOpsConfig.OperatorCSV) == 0 {
		output.Spec.GitOpsConfig.OperatorCSV = "v1.4.0"
	}
	if len(output.Spec.ClusterGroupName) == 0 {
		output.Spec.ClusterGroupName = "default"
	}

	if len(output.Status.Path) == 0 {
		output.Status.Path = filepath.Join(os.TempDir(), output.Namespace, output.Name)
	}

	return nil, output
}

func (r *PatternReconciler) finalizeObject(instance *api.Pattern) error {

	// Add finalizer when object is created
	log.Printf("Finalizing pattern object")

	// The object is being deleted
	if ContainsString(instance.ObjectMeta.Finalizers, api.PatternFinalizer) || ContainsString(instance.ObjectMeta.Finalizers, metav1.FinalizerOrphanDependents) {
		// Do any required cleanup here
		log.Printf("Removing the application, anything instantiated by ArgoCD can now be cleaned up manually")

		//		if err := removeApplication(p.Config, instance); err != nil {
		//			// Best effort only...
		//			r.logger.Info("Could not uninstall pattern", "error", err)
		//		}

		// Remove our finalizer from the list and update it.
		instance.ObjectMeta.Finalizers = RemoveString(instance.ObjectMeta.Finalizers, api.PatternFinalizer)
		err := r.Client.Update(context.Background(), instance)
		return err
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PatternReconciler) SetupWithManager(mgr ctrl.Manager) error {
	var err error
	r.config = mgr.GetConfig()

	if r.configClient, err = configclient.NewForConfig(r.config); err != nil {
		return err
	}

	//	if r.fullClient, err = kubernetes.NewForConfig(r.config); err != nil {
	//		return err
	//	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&api.Pattern{}).
		Complete(r)
}

func (r *PatternReconciler) onReconcileErrorWithRequeue(p *api.Pattern, reason string, err error, duration *time.Duration) (reconcile.Result, error) {
	// err is logged by the reconcileHandler
	p.Status.LastStep = reason
	if err != nil {
		p.Status.LastError = err.Error()
		log.Printf("\n\x1b[31;1m\tReconcile step %q failed: %s\x1b[0m\n", reason, err.Error())
		//r.logger.Error(fmt.Errorf("Reconcile step failed"), reason)
	} else {
		p.Status.LastError = ""
		log.Printf("\n\x1b[34;1m\tReconcile step %q complete\x1b[0m\n", reason)
	}

	updateErr := r.Client.Status().Update(context.TODO(), p)
	if updateErr != nil {
		r.logger.Error(updateErr, "Failed to update Pattern status")
	}
	if duration != nil {
		return reconcile.Result{RequeueAfter: *duration}, err
	}
	//	log.Printf("Reconciling with exponential duration")
	return reconcile.Result{}, err
}

func (r *PatternReconciler) actionPerformed(p *api.Pattern, reason string, err error) (reconcile.Result, error) {
	if err == nil {
		delay := time.Second * 5
		return r.onReconcileErrorWithRequeue(p, reason, err, &delay)
	}
	return r.onReconcileErrorWithRequeue(p, reason, err, nil)
}
