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
	"fmt"
	kbatch "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ref "k8s.io/client-go/tools/reference"
	"sort"
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
	var successfulJobs []*kbatch.Job
	var failedJobs []*kbatch.Job
	var mostRecentTime *time.Time

	// 检查job的状态类型
	isJobFinished := func(job *kbatch.Job) (bool, kbatch.JobConditionType) {
		for _, condition := range job.Status.Conditions {
			if (condition.Type == kbatch.JobComplete || condition.Type == kbatch.JobFailed) &&
				condition.Status == corev1.ConditionTrue {
				return true, condition.Type
			}
		}
		return false, ""
	}

	getScheduledTimeForJob := func(job *kbatch.Job) (*time.Time, error) {
		timeRow := job.Annotations[scheduledTimeAnnotation]
		if len(timeRow) == 0 {
			return nil, nil
		}

		timeParsed, err := time.Parse(time.RFC3339, timeRow)
		if err != nil {
			return nil, err
		}
		return &timeParsed, nil
	}

	for _, job := range childJobs.Items {
		_, finishedType := isJobFinished(&job)
		switch finishedType {
		case "":
			activeJobs = append(activeJobs, &job)
		case kbatch.JobFailed:
			failedJobs = append(failedJobs, &job)
		case kbatch.JobComplete:
			successfulJobs = append(successfulJobs, &job)
		}

		// 将作业启动时间存放在注释中
		scheduledTimeForJob, err := getScheduledTimeForJob(&job)
		if err != nil {
			log.Error(err, "无法解析子工作的计划时间", "job", &job)
			continue
		}
		if scheduledTimeForJob != nil {
			if mostRecentTime == nil {
				mostRecentTime = scheduledTimeForJob
			} else if mostRecentTime.Before(*scheduledTimeForJob) {
				mostRecentTime = scheduledTimeForJob
			}
		}
	}

	if mostRecentTime != nil {
		cronJob.Status.LastScheduleTime = &metav1.Time{Time: *mostRecentTime}
	} else {
		cronJob.Status.LastScheduleTime = nil
	}
	cronJob.Status.Active = nil

	for _, activeJob := range activeJobs {
		jobRef, err := ref.GetReference(r.Scheme, activeJob)
		if err != nil {
			log.Error(err, "无法引用正在进行的作业", "job", jobRef)
		}
		cronJob.Status.Active = append(cronJob.Status.Active, *jobRef)
	}

	log.V(1).
		Info("job count",
			"active jobs", len(activeJobs),
			"successful jobs", len(successfulJobs),
			"failed jobs", len(failedJobs))

	// 更新cronJob的status状态
	if err := r.Status().Update(ctx, &cronJob); err != nil {
		log.Error(err, "无法更新定时任务状态")
		return ctrl.Result{}, err
	}

	deleteLimitFunc := func(limit *int32, jobs []*kbatch.Job, state string) {
		if limit != nil {
			sort.Slice(jobs, func(i, j int) bool {
				if jobs[i].Status.StartTime == nil {
					return jobs[i].Status.StartTime != nil
				}
				return jobs[i].Status.StartTime.Before(jobs[j].Status.StartTime)
			})
			for i, job := range jobs {
				if i >= len(failedJobs)-int(*cronJob.Spec.FailedJobsHistoryLimit) {
					break
				}

				if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
					log.Error(err, fmt.Sprintf("无法删除%s的旧任务", state), "job", job)
				} else {
					log.V(0).Info(fmt.Sprintf("删除%s的旧任务", state), "job", job)
				}
			}
		}
	}

	// 删除失败的旧任务
	deleteLimitFunc(cronJob.Spec.FailedJobsHistoryLimit, failedJobs, "失败")
	// 删除成功的旧任务
	deleteLimitFunc(cronJob.Spec.SuccessfulJobsHistoryLimit, successfulJobs, "失败")

	if cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend == true {
		log.V(1).Info("定时任务被挂起，跳过执行")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *CronJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&batch.CronJob{}).
		Complete(r)
}
