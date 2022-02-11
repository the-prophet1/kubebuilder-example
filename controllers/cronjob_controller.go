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
	"github.com/go-logr/logr"
	"github.com/robfig/cron"
	batch "github.com/the-prophet/cronjob/api/v1"
	kbatch "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ref "k8s.io/client-go/tools/reference"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sort"
	"time"
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
	jobOwnerKey             = ".metadata.controller"
	apiGVStr                = batch.GroupVersion.String()
)

//+kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=batch.tutorial.kubebuilder.io,resources=cronjobs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the CronJob object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.2/pkg/reconcile
func (r *CronJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	getNextSchedule := func(cronjob *batch.CronJob, now time.Time) (lastMissed time.Time, next time.Time, err error) {
		sched, err := cron.ParseStandard(cronjob.Spec.Schedule)
		if err != nil {
			return time.Time{}, time.Time{},
				fmt.Errorf("无法解析时间表: %q: %v", cronJob.Spec.Schedule, err)
		}

		var earliestTime time.Time
		if cronjob.Status.LastScheduleTime != nil {
			earliestTime = cronJob.Status.LastScheduleTime.Time
		} else {
			earliestTime = cronJob.ObjectMeta.CreationTimestamp.Time
		}

		if cronjob.Spec.StartingDeadlineSeconds != nil {
			schedulingDeadline := now.Add(-time.Second * time.Duration(*cronJob.Spec.StartingDeadlineSeconds))
			if schedulingDeadline.After(earliestTime) {
				earliestTime = schedulingDeadline
			}

			if earliestTime.After(now) {
				return time.Time{}, sched.Next(now), nil
			}

			starts := 0
			for t := sched.Next(earliestTime); !t.After(now); t = sched.Next(t) {
				lastMissed = t
				starts++
				if starts > 100 {
					// 我们无法获得最近的时间，所以只返回一个空切片
					return time.Time{}, time.Time{}, fmt.Errorf("错过的开始时间太多 (> 100)。 设置或减少 .spec.startingDeadlineSeconds 或检查时钟偏差。")
				}
			}
		}
		// 返回最后错过的时间，和下一次调度的时间
		return lastMissed, sched.Next(now), nil
	}

	// 计算出定时任务下一次执行时间（或是遗漏的执行时间）
	missedRun, nextRun, err := getNextSchedule(&cronJob, r.Now())
	if err != nil {
		log.Error(err, "无法弄清楚 CronJob 时间表")
		// 在我们得到修复时间表的更新之前，我们并不真正关心重新排队，所以不要返回错误
		return ctrl.Result{}, nil
	}

	scheduledResult := ctrl.Result{RequeueAfter: nextRun.Sub(r.Now())} // 保存以便别处复用
	log = log.WithValues("now", r.Now(), "next run", nextRun)

	if missedRun.IsZero() {
		log.V(1).Info("没有即将到来的任务，进入睡眠")
		return scheduledResult, nil
	}

	// 确保错过的执行没有超过截止时间
	log = log.WithValues("current run", missedRun)
	tooLate := false
	if cronJob.Spec.StartingDeadlineSeconds != nil {
		tooLate = missedRun.Add(time.Duration(*cronJob.Spec.StartingDeadlineSeconds) * time.Second).Before(r.Now())
	}
	if tooLate {
		log.V(1).Info("missed starting deadline for last run, sleeping till next")
		return scheduledResult, nil
	}

	// 确定要 job 的执行策略 —— 并发策略可能禁止多个job同时运行
	if cronJob.Spec.ConcurrencyPolicy == batch.ForbidConcurrent && len(activeJobs) > 0 {
		log.V(1).Info("并发策略禁止并行进行，跳过", "正在执行的个数", len(activeJobs))
		return scheduledResult, nil
	}

	// 直接覆盖当前执行的job,则先删除现在运行的job
	if cronJob.Spec.ConcurrencyPolicy == batch.ReplaceConcurrent {
		for _, activeJob := range activeJobs {
			if err := r.Delete(ctx, activeJob, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// 构建新的job
	constructJobForCronJob := func(cronJob *batch.CronJob, scheduledTime time.Time) (*kbatch.Job, error) {
		//job 名称带上执行时间以确保唯一性，避免排定执行时间的 job 创建两次
		name := fmt.Sprintf("%s-%d", cronJob.Name, scheduledTime.Unix())

		job := &kbatch.Job{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      map[string]string{},
				Annotations: map[string]string{},
				Name:        name,
				Namespace:   cronJob.Namespace,
			},
			Spec: *cronJob.Spec.JobTemplate.Spec.DeepCopy(),
		}

		for k, v := range cronJob.Annotations {
			job.Annotations[k] = v
		}
		job.Annotations[scheduledTimeAnnotation] = scheduledTime.Format(time.RFC3339)
		for k, v := range cronJob.Spec.JobTemplate.Labels {
			job.Labels[k] = v
		}

		if err := ctrl.SetControllerReference(cronJob, job, r.Scheme); err != nil {
			return nil, err
		}
		return job, nil
	}

	job, err := constructJobForCronJob(&cronJob, missedRun)
	if err != nil {
		log.Error(err, "无法从模板构建job”")
		// job 的 spec 没有变更，无需重新排队
		return scheduledResult, nil
	}

	// ...在集群中创建 job
	if err := r.Create(ctx, job); err != nil {
		log.Error(err, "不允许构建job", "job", job)
		return ctrl.Result{}, err
	}

	log.V(1).Info("为 CronJob 运行创建job", "job", job)

	// 当有 job 进入运行状态后，重新排队，同时更新状态
	return scheduledResult, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CronJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// 此处不是测试，我们需要创建一个真实的时钟
	if r.Clock == nil {
		r.Clock = realClock{}
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &kbatch.Job{}, jobOwnerKey, func(rawObj client.Object) []string {
		//获取 job 对象，提取 owner...
		job := rawObj.(*kbatch.Job)
		// 返回job 对应的控制器
		owner := metav1.GetControllerOf(job)
		if owner == nil {
			return nil
		}
		// ...确保 owner 是个 CronJob...
		if owner.APIVersion != apiGVStr || owner.Kind != "CronJob" {
			return nil
		}

		// ...是 CronJob，返回
		return []string{owner.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&batch.CronJob{}).
		Complete(r)
}
