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

package v1

import (
	batchv1beta1 "k8s.io/api/batch/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ConcurrencyPolicy描述了如何处理作业
// 只能指定以下并发策略之一
// 如果不指定以下策略，则使用默认策略:AllowConcurrent
// +kubebuilder:validation:Enum=Allow;Forbid;Replace
type ConcurrencyPolicy string

const (

	// AllowConcurrent 允许CronJobs并发运行。
	AllowConcurrent ConcurrencyPolicy = "Allow"

	// ForbidConcurrent 禁止并发运行，如果上一个尚未完成则跳过下一个运行。
	ForbidConcurrent ConcurrencyPolicy = "Forbid"

	// ReplaceConcurrent 取消当前正在运行的作业，并用一个新的作业替换它。
	ReplaceConcurrent ConcurrencyPolicy = "Replace"
)

// CronJobSpec defines the desired state of CronJob
type CronJobSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	//Cron格式的时间表
	Schedule string `json:"schedule"`

	// 开始工作的截止日期，单位为秒
	// 错过的任务被认为为执行失败
	// +optional
	StartingDeadlineSeconds *int64 `json:"startingDeadlineSeconds,omitempty"`

	// 指定如何处理作业的并发执行。
	// 可选值:
	// - "Allow" (默认值): 允许所有任务并发执行
	// - "Forbid": 禁止并发运行，如果前一次运行尚未结束，则跳过下一次运行
	// - "Replace": 取消当前正在运行的作业并将其替换为新作业
	// +optional
	ConcurrencyPolicy ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// 此标志告诉控制器暂停后续执行，已执行的不受影响
	// +optional
	Suspend *bool `json:"suspend,omitempty"`

	// 指定在执行CronJob时创建的任务。
	JobTemplate batchv1beta1.JobTemplateSpec `json:"jobTemplate"`

	// 成功工作数量的历史记录个数。
	// +optional
	SuccessfulJobsHistoryLimit *int32 `json:"successfulJobsHistoryLimit,omitempty"`

	// 失败工作数量的历史记录个数
	// +optional
	FailedJobsHistoryLimit *int32 `json:"failedJobsHistoryLimit,omitempty"`
}

// CronJobStatus 定义 CronJob 的观察状态
type CronJobStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// 指向当前运行作业的指针列表。
	// +optional
	Active []corev1.ObjectReference `json:"active,omitempty"`

	// 最后一次成功调度作业的时间信息。
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime"`
}

// +kubebuilder:object:root=true

// CronJob is the Schema for the cronjobs API
type CronJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CronJobSpec   `json:"spec,omitempty"`
	Status CronJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CronJobList contains a list of CronJob
type CronJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CronJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CronJob{}, &CronJobList{})
}
