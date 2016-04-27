package main

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/golang/glog"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/client/restclient"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/util/wait"

	"k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/controller/framework"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/runtime"
)

type KubeWorker struct {
	client        *kclient.Client
	pods          cache.StoreToPodLister
	podController *framework.Controller
}

type NodeEmptyCallback func(node *api.Node)

const (
	DefaultAPIHost = "localhost"
	DefaultAPIPort = "8080"
	APIHostParam   = "api-server"
	APIPortParam   = "api-port"
)

func getAPIAddress(config ConfigInfo) string {
	var host, port, scheme string
	var exists bool

	if host, exists = config[APIHostParam]; exists == false || host == "" {
		host = DefaultAPIHost
	}

	if port, exists = config[APIPortParam]; exists == false || port == "" {
		port = DefaultAPIPort
	}

	scheme = "http"
	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}

func NewKubeWorker(config ConfigInfo) *KubeWorker {
	client, err := kclient.New(&restclient.Config{
		Host: getAPIAddress(config),
	})

	if err != nil {
		panic(err)
	}

	return &KubeWorker{
		client: client,
	}
}

func (k *KubeWorker) findNodeByName(name string) (*api.Node, error) {
	return k.client.Nodes().Get(name)
}

func (k *KubeWorker) findNodeByAddress(address string) (api.Node, error) {
	nodes, err := k.client.Nodes().List(api.ListOptions{
		LabelSelector: labels.Everything(),
		FieldSelector: fields.Everything(),
	})

	if err != nil {
		return api.Node{}, err
	}

	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			if a.Address == address {
				return n, nil
			}
		}
	}

	return api.Node{}, errors.New("Unable to find node by address")
}

func isPodDaemonset(pod *api.Pod) bool {
	//Same process as kubectl drain
	creatorRef, found := pod.ObjectMeta.Annotations[controller.CreatedByAnnotation]

	if found {
		// Now verify that the specified creator actually exists.
		var sr api.SerializedReference
		if err := runtime.DecodeInto(api.Codecs.UniversalDecoder(), []byte(creatorRef), &sr); err != nil {
			glog.Warningf("Pod: %s claimed to have the CreatedByAnnotation. Decoding it failed.", pod.Name)
			return false
		}

		if sr.Reference.Kind == "DaemonSet" {
			//Skipping any validation.
			//TODO Consider not taking pods at their word. They lie.
			return true
		}
	}
	return false
}

func setTimerIfEmpty(store cache.StoreToPodLister, timer *time.Timer, duration *time.Duration) {
	re := regexp.MustCompile("gcr.io/google_containers/pause")
	pods, _ := store.List(labels.Everything())
	for _, p := range pods {
		if p.Status.Phase == api.PodRunning || p.Status.Phase == api.PodPending {
			//Ignore reservation pods
			if len(p.Spec.Containers) == 1 {
				if re.FindStringIndex(p.Spec.Containers[0].Image) != nil {
					continue
				}
			}

			//Ignore DaemonSets
			if isPodDaemonset(p) {
				continue
			}

			glog.Info("Pod Alive:", p.Name)
			return
		}
	}
	glog.Info("Pods Empty. Setting Timer")
	timer.Reset(*duration)
}

func (k *KubeWorker) createWatcher(node *api.Node, terminateTime *time.Duration, callback NodeEmptyCallback) {

	f := fields.Set{
		"spec.nodeName": node.Name}
	lw := cache.NewListWatchFromClient(k.client, "pods", api.NamespaceAll, fields.SelectorFromSet(f))

	timer := time.NewTimer(10 * time.Minute) //Initial Time doesn't matter
	timer.Stop()

	go func() {
		<-timer.C
		callback(node)
	}()

	k.pods.Store, k.podController = framework.NewInformer(
		lw,
		&api.Pod{},
		0,
		framework.ResourceEventHandlerFuncs{
			AddFunc: func(new interface{}) {
				glog.Info("Pod Added. Stopping timer")
				timer.Stop()
			},
			DeleteFunc: func(old interface{}) {
				setTimerIfEmpty(k.pods, timer, terminateTime)
			},
		},
	)

	go k.podController.Run(wait.NeverStop)
	//Wait for initial sync
	for k.podController.HasSynced() == false {
		time.Sleep(30 * time.Second)
	}
	glog.Info("Initial sync complete")

	setTimerIfEmpty(k.pods, timer, terminateTime)
}

func (k *KubeWorker) WatchNodeByName(name string, terminateTime *time.Duration, callback NodeEmptyCallback) error {
	node, err := k.findNodeByName(name)

	if err != nil {
		return err
	}

	k.createWatcher(node, terminateTime, callback)

	return nil
}

func (k *KubeWorker) WatchNodeByAddress(address string, terminateTime *time.Duration, callback NodeEmptyCallback) error {
	node, err := k.findNodeByAddress(address)

	if err != nil {
		return err
	}
	glog.Info(node.Name)
	glog.Info(k.client.ServerVersion())

	k.createWatcher(&node, terminateTime, callback)
	return nil
}

func (k *KubeWorker) MarkUnschedulable(node *api.Node) {
	node.Spec.Unschedulable = true
	k.client.Nodes().Update(node)
}
