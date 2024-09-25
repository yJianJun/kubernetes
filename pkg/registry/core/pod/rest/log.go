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

// LogREST 实现 Pod 的日志Rest接口
type LogREST struct {
	//类型为 client.ConnectionInfoGetter 的接口，
	//用于获取指定节点上 kubelet 的连接信息。
	KubeletConn client.ConnectionInfoGetter
	//指向 genericregistry.Store 的指针，用于存储和管理 Pod 日志信息。
	Store *genericregistry.Store
}

// _ 用于确保LogREST类型满足GetterWithOptions接口。 它用作编译时检查，
// 保证 LogREST 实现GetterWithOptions接口需要的所有方法 。
var _ = rest.GetterWithOptions(&LogREST{})

// New creates a new Pod log options object
// LogREST 类型的 New 方法目前创建并返回了一个 api.Pod 对象。但是，注释指出，
// 最终这个方法应该返回一个更具体的表示日志的资源。
func (r *LogREST) New() runtime.Object {
	// TODO - return a resource that represents a log
	return &api.Pod{}
}

// Destroy 在关闭时清理资源。
// Destroy 函数之所以没有具体清理动作，是因为底层的存储资源与 REST API 共用，避免重复销毁。
// 其余与 LogREST 相关的方法和结构体提供了如何获取和处理 Pod 日志的完整逻辑。
func (r *LogREST) Destroy() {
	// 鉴于底层存储与 REST 共享， 我们不会在这里显式地销毁它。
}

// ProducesMIMETypes 返回指定 HTTP 动词（GET、POST、DELETE、PATCH）可以响应的 MIME 类型列表。
func (r *LogREST) ProducesMIMETypes(verb string) []string {
	// 由于默认列表没有 "plain/text"，我们需要显式覆盖 ProducesMIMETypes，
	// 以便把它添加到 pods/{name}/log 的 "produces" 部分
	return []string{
		"text/plain",
	}
}

// 根据指定的 HTTP 动词返回一个对象。这些对象用于响应 HTTP 请求。尽管方法返回的对象类型是通用的 interface{}，
// 真正重要的是对象的类型，而不是其值。在这个例子中，返回的是一个空字符串 ""。
func (r *LogREST) ProducesObject(verb string) interface{} {
	return ""
}

/*
*
Get 检索将流式传输 pod 日志内容的runtime.Object
根据传入的日志选项，检索Pod的日志信息，并流式传输这些日志。
通过一系列操作（注册度量、选项验证、获取日志位置和传输方式），最终构建并返回一个LocationStreamer对象。
*/
func (r *LogREST) Get(ctx context.Context, name string, opts runtime.Object) (runtime.Object, error) {
	// 如果上下文被使用，则注册度量。假设 sync.Once 是快速的。如果不是，它可能是一个初始化块。
	registerMetrics()

	// 类型断言，将 opts 转换为 *api.PodLogOptions 类型
	logOpts, ok := opts.(*api.PodLogOptions)
	if !ok {
		return nil, fmt.Errorf("invalid options object: %#v", opts)
	}

	// 计算是否跳过 TLS 校验的度量
	countSkipTLSMetric(logOpts.InsecureSkipTLSVerifyBackend)

	// 验证 PodLogOptions，如果有错误，返回无效错误
	if errs := validation.ValidatePodLogOptions(logOpts); len(errs) > 0 {
		return nil, errors.NewInvalid(api.Kind("PodLogOptions"), name, errs)
	}

	// 获取日志的位置和传输信息
	location, transport, err := pod.LogLocation(ctx, r.Store, r.KubeletConn, name, logOpts)
	if err != nil {
		return nil, err
	}

	// 返回一个 LocationStreamer 对象，它包含了流式传输日志所需的信息
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

/*
*
根据传入的 insecureSkipTLSVerifyBackend 参数来更新存储 TLS 跳过验证使用情况的计数器。
本代码的作用是对不同类型的 TLS 配置使用情况进行计数，并记录在度量指标中
*/
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

/*
*
创建并返回一个新的Pod日志查询选项对象，并且为其他两个返回值提供默认值false和空字符串。
*/
func (r *LogREST) NewGetOptions() (runtime.Object, bool, string) {
	return &api.PodLogOptions{}, false, ""
}

/*
*
OverrideMetricsVerb 将 GET 谓词覆盖为 CONNECT 以获取 pod 日志资源
Pod 日志资源的操作：当对 Pod 日志资源发出 HTTP "GET" 请求时，通过将动词修改为 "CONNECT"，
可以实现特定的逻辑或优化。例如，HTTP "CONNECT" 方法通常用于启动双向通信通道，适合于需要长时间或持续连接的场景。
覆盖默认行为：有时候需要改变默认的 HTTP 动词行为以适应特定的需求。通过这个方法，可以动态改变某些请求的处理方式。
*/
func (r *LogREST) OverrideMetricsVerb(oldVerb string) (newVerb string) {
	newVerb = oldVerb

	if oldVerb == "GET" {
		newVerb = "CONNECT"
	}

	return
}
