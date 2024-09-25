/*
Copyright 2014 The Kubernetes Authors.

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

package rest

import (
	"context"
	"fmt"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	genericrest "k8s.io/apiserver/pkg/registry/generic/rest"
	"k8s.io/apiserver/pkg/registry/rest"
	api "k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/kubernetes/pkg/apis/core/validation"
	"k8s.io/kubernetes/pkg/kubelet/client"
	"k8s.io/kubernetes/pkg/registry/core/pod"

	// ensure types are installed
	_ "k8s.io/kubernetes/pkg/apis/core/install"
)

// LogREST 实现 Pod 的日志端点
type LogREST struct {
	KubeletConn client.ConnectionInfoGetter
	Store       *genericregistry.Store
}

// LogREST 实现 GetterWithOptions
var _ = rest.GetterWithOptions(&LogREST{})

// New 创建一个新的 Pod 日志选项对象
func (r *LogREST) New() runtime.Object {
	// TODO - return a resource that represents a log
	return &api.Pod{}
}

// Destroy 在关闭时清理资源。
func (r *LogREST) Destroy() {
	// Given that underlying store is shared with REST,
	// we don't destroy it here explicitly.
}

// ProducesMIMETypes 返回指定 HTTP 动词（GET、POST、DELETE、 PATCH) 可以响应。
func (r *LogREST) ProducesMIMETypes(verb string) []string {
	// 由于默认列表不包含“plain/text”，我们需要显式覆盖 ProducesMIMETypes，以便将其添加到pods/{name}/log 的“生产”部分
	return []string{
		"text/plain",
	}
}

// ProducesObject 返回指定 HTTP 动词响应的对象。如果出现以下情况，它将覆盖存储对象：
// 它不为零。只有返回对象的类型很重要，该值将被忽略。
func (r *LogREST) ProducesObject(verb string) interface{} {
	return ""
}

// Get 检索将流式传输 pod 日志内容的runtime.Object
func (r *LogREST) Get(ctx context.Context, name string, opts runtime.Object) (runtime.Object, error) {
	// 如果使用了上下文，则注册指标。  这假设sync.Once 很快。  如果不是，它可能是一个初始化块。
	registerMetrics()

	logOpts, ok := opts.(*api.PodLogOptions)
	if !ok {
		return nil, fmt.Errorf("invalid options object: %#v", opts)
	}

	countSkipTLSMetric(logOpts.InsecureSkipTLSVerifyBackend)

	if errs := validation.ValidatePodLogOptions(logOpts); len(errs) > 0 {
		return nil, errors.NewInvalid(api.Kind("PodLogOptions"), name, errs)
	}
	location, transport, err := pod.LogLocation(ctx, r.Store, r.KubeletConn, name, logOpts)
	if err != nil {
		return nil, err
	}
	return &genericrest.LocationStreamer{
		Location:                              location,
		Transport:                             transport,
		ContentType:                           "text/plain",
		Flush:                                 logOpts.Follow,
		ResponseChecker:                       genericrest.NewGenericHttpResponseChecker(api.Resource("pods/log"), name),
		RedirectChecker:                       genericrest.PreventRedirects,
		TLSVerificationErrorCounter:           podLogsTLSFailure,
		DeprecatedTLSVerificationErrorCounter: deprecatedPodLogsTLSFailure,
	}, nil
}

func countSkipTLSMetric(insecureSkipTLSVerifyBackend bool) {
	usageType := usageEnforce
	if insecureSkipTLSVerifyBackend {
		usageType = usageSkipAllowed
	}

	counter, err := podLogsUsage.GetMetricWithLabelValues(usageType)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	counter.Inc()

	deprecatedPodLogsUsage.WithLabelValues(usageType).Inc()
}

// NewGetOptions 创建一个新的选项对象
func (r *LogREST) NewGetOptions() (runtime.Object, bool, string) {
	return &api.PodLogOptions{}, false, ""
}

// OverrideMetricsVerb 将 GET 谓词覆盖为 CONNECT 以获取 pod 日志资源
func (r *LogREST) OverrideMetricsVerb(oldVerb string) (newVerb string) {
	newVerb = oldVerb

	if oldVerb == "GET" {
		newVerb = "CONNECT"
	}

	return
}
