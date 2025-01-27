package controller

import (
	"context"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	quickstartv1 "quickstart.com/api/v1"
	metrics "quickstart.com/internal/metrics"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	apiServerImageBaseName = "api-server"
	replicas               = 2
	containerPort          = 8089
	deploymentPrefix       = "deployment"
	servicePrefix          = "service"
	version                = "version"
	finalizerName          = "apiserver.quickstart.quickstart.com-v1-finalizer"
)

// APIServerReconciler reconciles a APIServer object
type APIServerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=quickstart.quickstart.com,resources=apiservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=quickstart.quickstart.com,resources=apiservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=quickstart.quickstart.com,resources=apiservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;list;watch;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the APIServer object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/reconcile
func (r *APIServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// lets first get the resource from k8s
	apiServerCr := &quickstartv1.APIServer{}
	if err := r.Get(ctx, req.NamespacedName, apiServerCr); err != nil {
		if apierr.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			log.Info("AP Server resource not found. May be removed")
			return ctrl.Result{}, nil
		}

		log.Error(err, "Failed to get APIServer resource")
		return ctrl.Result{}, err
	}
	log.Info("Read API Server", "version", apiServerCr.Spec.Version, "namespace", apiServerCr.ObjectMeta.Namespace)

	if apiServerCr.ObjectMeta.DeletionTimestamp.IsZero() {
		// Resource is not being deleted, so add finalizer if it does not have one and update the resource.
		if !controllerutil.ContainsFinalizer(apiServerCr, finalizerName) {
			controllerutil.AddFinalizer(apiServerCr, finalizerName)
			if finalizerErr := r.Update(ctx, apiServerCr); finalizerErr != nil {
				log.Info("Failed to add finalizer")
				return ctrl.Result{}, finalizerErr
			}
		}
	} else {
		// Resource is being deleted, perform cleanup actions

		// 1. Check if our finalizer is present on the resource
		if controllerutil.ContainsFinalizer(apiServerCr, finalizerName) {
			// 2. Do the cleanup
			if cleanupErr := r.DoCleanup(ctx); cleanupErr != nil {
				// Fail to delete the external dependency , return with error
				// so that it can be retried.
				return ctrl.Result{}, cleanupErr
			}
			// 3. Remove the finalizer from the resource and update it
			controllerutil.RemoveFinalizer(apiServerCr, finalizerName)
			if removeFinalizerErr := r.Update(ctx, apiServerCr); removeFinalizerErr != nil {
				return ctrl.Result{}, removeFinalizerErr
			}
		}
		// 4. Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	// Reconciliation logic
	// If namespace doesn't exist, error out
	_, err := r.IsNamespaceExists(ctx, req)
	if err != nil {
		log.Info("Reconciling API Server failed as namespace doesn't exist",
			"version", apiServerCr.Spec.Version,
			"namespace", apiServerCr.ObjectMeta.Namespace)
		return ctrl.Result{}, err
	}

	// Check if deployment exists
	deployment, errDep := r.GetDeployment(ctx, req, apiServerCr)
	if errDep != nil {
		log.Info("Reconciling API Server failed as unable to check if Deployment exists ",
			"version", apiServerCr.Spec.Version,
			"namespace", apiServerCr.ObjectMeta.Namespace)
		return ctrl.Result{}, errDep
	}
	isDeploymentExists := deployment != nil

	if isDeploymentExists == false {
		log.Info("Deployment doesn't exist. Creting new one")
		// Create a deployment
		addDepErr := r.AddDeployment(ctx, apiServerCr)
		if addDepErr != nil {
			log.Info("Failed to reoncile as add deployment failed")
			r.Recorder.Event(apiServerCr, corev1.EventTypeWarning, "DeploymentCreationFailed", "Failed to create Deployment")
			return ctrl.Result{}, addDepErr
		}
		r.Recorder.Event(apiServerCr, corev1.EventTypeNormal, "DeploymentCreated", "Successfully created Deployment")
	} else {
		log.Info("Deployment exists", "deployment", deployment.Name)
		// Update the deployment
		updateDepErr := r.UpdateDeployment(ctx,
			apiServerCr,
			deployment,
		)
		if updateDepErr != nil {
			log.Info("Failed to reoncile as update deployment failed")
			r.Recorder.Event(apiServerCr, corev1.EventTypeWarning, "DeploymentUpdateFailed", "Failed to update Deployment")
			return ctrl.Result{}, updateDepErr
		}
		r.Recorder.Event(apiServerCr, corev1.EventTypeNormal, "DeploymentUpdated", "Successfully updated Deployment")
	}

	// Check if service exists
	service, errDep := r.GetService(ctx, req, apiServerCr)
	if errDep != nil {
		log.Info("Reconciling API Server failed as unable to check if Service exists ",
			"version", apiServerCr.Spec.Version,
			"namespace", apiServerCr.ObjectMeta.Namespace)
		return ctrl.Result{}, errDep
	}
	isServiceExists := service != nil

	if isServiceExists == false {
		log.Info("Service doesn't exist. Creting new one")
		// Create a service
		addSvcErr := r.AddService(ctx, apiServerCr)
		if addSvcErr != nil {
			log.Info("Failed to reoncile as add service failed")
			r.Recorder.Event(apiServerCr, corev1.EventTypeWarning, "ServiceCreationFailed", "Failed to create Service")
			return ctrl.Result{}, addSvcErr
		}
		r.Recorder.Event(apiServerCr, corev1.EventTypeNormal, "ServiceCreated", "Successfully created Service")
	} else {
		log.Info("Service exists. Updating...", "service", service.Name)
		// Update the service
		updateSvcErr := r.UpdateService(ctx,
			apiServerCr,
			service,
		)
		if updateSvcErr != nil {
			log.Info("Failed to reoncile as update service failed")
			r.Recorder.Event(apiServerCr, corev1.EventTypeWarning, "ServiceUpdateFailed", "Failed to update Service")
			return ctrl.Result{}, updateSvcErr
		}
		r.Recorder.Event(apiServerCr, corev1.EventTypeNormal, "ServiceUpdated", "Successfully updated Service")
	}

	log.Info("Reconciliation complete")
	r.Recorder.Event(apiServerCr, corev1.EventTypeNormal, "Reconciled", "Successfully reconciled API Server")
	metrics.APIServerControllerSuccessTotal.WithLabelValues(apiServerCr.Spec.Version, apiServerCr.ObjectMeta.Namespace).Inc()
	return ctrl.Result{}, nil

}

// IsNamespaceExists Checks if namespace exists in the cluster
func (r *APIServerReconciler) IsNamespaceExists(ctx context.Context, req ctrl.Request) (bool, error) {
	namespace := req.Namespace
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		if errors.IsNotFound(err) {
			return false, nil // Namespace does not exist
		}
		return false, err // Other errors
	}
	return true, nil
}

// GetDeployment returns a deployment id it exists else nil
func (r *APIServerReconciler) GetDeployment(ctx context.Context,
	req ctrl.Request,
	apiServer *quickstartv1.APIServer) (*appsv1.Deployment, error) {
	log := log.FromContext(ctx)

	// construct deployment name: deployment_namespace_version
	deploymentName := r.GetDeploymentName()
	log.Info(deploymentName)

	// check if deployment exists in the namespace in cluster
	deployment := &appsv1.Deployment{}

	if err := r.Get(ctx, types.NamespacedName{
		Namespace: req.Namespace,
		Name:      deploymentName,
	}, deployment); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil // Deployment does not exist
		}
		return nil, err // Other errors
	}
	return deployment, nil
}

// AddDeployment adds a deployment
func (r *APIServerReconciler) AddDeployment(ctx context.Context, apiServerCr *quickstartv1.APIServer,
) error {
	labels := map[string]string{
		"app":   r.GetAppName(),
		version: apiServerCr.Spec.Version,
	}
	selectorLabels := map[string]string{
		"app": r.GetAppName(),
	}

	deploymentSpec := r.CreateDeploymentSpec(r.GetDeploymentName(), apiServerCr.ObjectMeta.Namespace,
		r.GetAppName(), r.GetImageName(apiServerCr.Spec.Version),
		int32(replicas), int32(containerPort), labels, selectorLabels)

	// Set the owner reference
	if err := controllerutil.SetControllerReference(apiServerCr, deploymentSpec, r.Scheme); err != nil {
		return err
	}
	return r.Client.Create(ctx, deploymentSpec)
}

// UpdateDeployment updates deployment if required
func (r *APIServerReconciler) UpdateDeployment(ctx context.Context,
	apiServerCR *quickstartv1.APIServer,
	actualDeployment *appsv1.Deployment,
) error {
	log := log.FromContext(ctx)
	// check if anything has changed in the deployment. Check
	actualDeploymentVersion, _ := actualDeployment.ObjectMeta.Labels[version]
	desiredDeploymentVersion := apiServerCR.Spec.Version
	if actualDeploymentVersion == desiredDeploymentVersion {
		log.Info("Deployment is unchanged")
		return nil
	}

	// Update Deployment labels
	if actualDeployment.Labels == nil {
		actualDeployment.Labels = make(map[string]string)
	}

	actualDeployment.Labels[version] = desiredDeploymentVersion
	actualDeployment.Spec.Template.ObjectMeta.Labels[version] = desiredDeploymentVersion
	actualDeployment.Spec.Template.Spec.Containers[0].Image = r.GetImageName(desiredDeploymentVersion)

	return r.Client.Update(ctx, actualDeployment)
}

// GetDeploymentName returns deployment name
func (r *APIServerReconciler) GetDeploymentName() string {
	return apiServerImageBaseName + "-" + deploymentPrefix
}

// GetImageName gets an image name based on the version
func (r *APIServerReconciler) GetImageName(version string) string {
	return apiServerImageBaseName + ":" + version
}

// GetServiceName returns service name
func (r *APIServerReconciler) GetServiceName() string {
	return apiServerImageBaseName + "-" + servicePrefix
}

// GetAppName returns app name
func (r *APIServerReconciler) GetAppName() string {
	return apiServerImageBaseName
}

// CreateDeploymentSpec returns a Deployment spec for the given parameters.
func (r *APIServerReconciler) CreateDeploymentSpec(name string, namespace string,
	imageName string, image string, replicas int32, containerPort int32,
	labels map[string]string, selectorLabels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  name,
							Image: image,
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: containerPort,
								},
							},
						},
					},
				},
			},
		},
	}
}

// GetService returns a service id it exists else nil
func (r *APIServerReconciler) GetService(ctx context.Context,
	req ctrl.Request,
	apiServer *quickstartv1.APIServer) (*corev1.Service, error) {
	log := log.FromContext(ctx)

	// construct service name
	serviceName := r.GetServiceName()
	log.Info(serviceName)

	// check if serviceName exists in the namespace in cluster
	service := &corev1.Service{}

	if err := r.Get(ctx, types.NamespacedName{
		Namespace: req.Namespace,
		Name:      serviceName,
	}, service); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil // Service does not exist
		}
		return nil, err // Other errors
	}
	return service, nil
}

// CreateServiceSpec returns a Service spec for the given parameters.
func (r *APIServerReconciler) CreateServiceSpec(name string, namespace string, labels map[string]string,
	port int32, targetPort int32, selectorLabels map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: selectorLabels,
			Ports: []corev1.ServicePort{
				{
					Port:       port,
					TargetPort: intstr.FromInt(int(targetPort)),
					NodePort:   30000,
				},
			},
		},
	}
}

// AddService adds a service
func (r *APIServerReconciler) AddService(ctx context.Context,
	apiServerCr *quickstartv1.APIServer) error {
	labels := map[string]string{
		"app":   r.GetAppName(),
		version: apiServerCr.Spec.Version,
	}
	selectorLabels := map[string]string{
		"app": r.GetAppName(),
	}

	serviceSpec := r.CreateServiceSpec(r.GetServiceName(), apiServerCr.ObjectMeta.Namespace,
		labels, int32(containerPort), int32(containerPort), selectorLabels)

	// Set the owner reference
	if err := controllerutil.SetControllerReference(apiServerCr, serviceSpec, r.Scheme); err != nil {
		return err
	}
	return r.Client.Create(ctx, serviceSpec)
}

// UpdateService updates service if required
func (r *APIServerReconciler) UpdateService(ctx context.Context,
	apiServerCR *quickstartv1.APIServer,
	actualService *corev1.Service,
) error {
	log := log.FromContext(ctx)
	// check if anything has changed in the deployment. Check
	actualServiceVersion, _ := actualService.ObjectMeta.Labels[version]
	desiredServiceVersion := apiServerCR.Spec.Version
	if actualServiceVersion == desiredServiceVersion {
		log.Info("Service is unchanged")
		return nil
	}
	labels := map[string]string{
		version: desiredServiceVersion,
	}
	actualService.Labels = labels
	return r.Client.Update(ctx, actualService)
}

// DoCleanup cleans up the resource by removing external resources
func (r *APIServerReconciler) DoCleanup(ctx context.Context) error {
	log := log.FromContext(ctx)
	log.Info("Doing resource cleanup....")
	time.Sleep(10 * time.Second)
	log.Info("Resource cleanup complete.")
	return nil
}

// ReplaceDotWithDash replaces . with -
func (r *APIServerReconciler) ReplaceDotWithDash(input string) string {
	return strings.ReplaceAll(input, ".", "-")
}

// SetupWithManager sets up the controller with the Manager.
func (r *APIServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Scheme = mgr.GetScheme()
	r.Recorder = mgr.GetEventRecorderFor("apiserver-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&quickstartv1.APIServer{}).
		Named("apiserver").
		Complete(r)
}
