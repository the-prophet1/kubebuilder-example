/*


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
	kbatch "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	batch "github.com/the-prophet/cronjob/api/v1"
)

type realClock struct{}

func (_ realClock) Now() time.Time {
	return time.Now()
}

type Clock interface {
	Now() time.Time
}

// CronJobReconciler reconciles a CronJob object
type CronJobReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
	Clock
}

var (
	scheduledTimeAnnotation = "batch.tutorial.kubebuilder.io/scheduled-at"
	jobOwnerKey             = ""
)

// +kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
func (r *CronJobReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("cronjob", req.NamespacedName)

	var cronJob batch.CronJob
	if err := r.Get(ctx, req.NamespacedName, &cronJob); err != nil {
		log.Error(err, "无法获取到CronJob")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var childJobs kbatch.JobList
	if err := r.List(ctx, &childJobs, client.InNamespace(req.Namespace), client.MatchingFields{jobOwnerKey: req.Name}); err != nil {
		log.Error(err, "无法列出子作业")
		return ctrl.Result{}, err
	}

	// 列出所有有效的job
	var activeJobs []*kbatch.Job
	var successJobs []*kbatch.Job
	var failedJobs []*kbatch.Job
	var mostRecentTime *time.Time

	// 检查job的状态类型
	isJobFinished := func(job *kbatch.Job) (bool, kbatch.JobConditionType) {
		for _, condition := range job.Status.Conditions {
			if (condition.Type == kbatch.JobComplete || condition.Type == kbatch.JobFailed) && condition.Status == corev1.ConditionTrue {
				return true, condition.Type
			}
		}
		return false, ""
	}

	for i, job := range childJobs.Items {
		 _, finishedType := isJobFinished(&job)
		switch finishedType {
		case "":
			activeJobs = append(activeJobs,&job)
		case kbatch.JobFailed:
			failedJobs = append(failedJobs, &job)
		case kbatch.JobComplete:
			successJobs = append(successJobs, &job)
		}

	}

	return ctrl.Result{}, nil
}

func (r *CronJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&batch.CronJob{}).
		Complete(r)
}
