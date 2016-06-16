package main

import (
	"errors"
	"regexp"
	"sync"
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

//KubeWorker operates on a kubernetes cluster
type KubeWorker struct {
	client        *kclient.Client
	pods          cache.StoreToPodLister
	podController *framework.Controller
}

//NodeEmptyCallback represents the function to call when then node is empty
type NodeEmptyCallback func(node *api.Node)

const (
	//APIURLParam is the parameter used to specify the api server url from the command line
	APIURLParam = "api-server-url"
)

func newKubeWorker(config ConfigInfo) *KubeWorker {
	var restConfig *restclient.Config

	if config[APIURLParam] == "" {
		glog.Info("No url specified. Falling back to in cluster config")
		var cerr error
		restConfig, cerr = restclient.InClusterConfig()

		if cerr != nil {
			panic(cerr)
		}
	} else {
		restConfig = &restclient.Config{
			Host: config[APIURLParam],
		}
	}

	client, err := kclient.New(restConfig)

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

type killTimer struct {
	timer    *time.Timer
	timerSet bool
	lock     sync.Mutex
	C        <-chan time.Time
}

//StopIfRunning stops the wrapped timer if it is currently set
func (k *killTimer) StopIfRunning() {
	k.lock.Lock()
	defer k.lock.Unlock()

	if k.timerSet {
		k.timer.Stop()
		k.timerSet = false
	}
}

//ResetIfNotRunning restes the wrapper timer if it is not already running
func (k *killTimer) ResetIfNotRunning(duration time.Duration) {
	k.lock.Lock()
	defer k.lock.Unlock()

	if !k.timerSet {
		glog.Infon("Setting kill timer for %v \n", duration)
		k.timer.Reset(duration)
		k.timerSet = true
	}

}

func newKillTimer() *killTimer {
	timer := &killTimer{
		timer:    time.NewTimer(10 * time.Minute),
		timerSet: false, //We will stop it immedially following this
	}
	timer.timer.Stop()
	timer.C = timer.timer.C
	return timer
}

func isNodeEmpty(store cache.StoreToPodLister) bool {
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

			glog.V(2).Info("Pod Alive:", p.Name)
			return false
		}
	}
	glog.Info("Pods Empty.")
	return true
}

func (k *KubeWorker) createWatcher(node *api.Node, terminateTime *time.Duration, callback NodeEmptyCallback) {

	f := fields.Set{
		"spec.nodeName": node.Name}
	lw := cache.NewListWatchFromClient(k.client, "pods", api.NamespaceAll, fields.SelectorFromSet(f))

	k.pods.Store, k.podController = framework.NewInformer(
		lw,
		&api.Pod{},
		0,
		framework.ResourceEventHandlerFuncs{},
	)

	go k.podController.Run(wait.NeverStop)
	//Wait for initial sync
	for k.podController.HasSynced() == false {
		time.Sleep(30 * time.Second)
	}
	glog.Info("Initial sync complete")

	terminateTimer := newKillTimer()
	go func() {
		<-terminateTimer.C
		callback(node)
	}()

	ticker := time.NewTicker(time.Minute)
	go func() {
		for t := range ticker.C {
			glog.V(3).Info("Checking pods at ", t)
			if isNodeEmpty(k.pods) {
				terminateTimer.ResetIfNotRunning(time.Minute * *terminateTime)
			} else {
				terminateTimer.StopIfRunning()
			}
		}
	}()
}

//WatchNodeByName creates a watcher based on nodename given
func (k *KubeWorker) WatchNodeByName(name string, terminateTime *time.Duration, callback NodeEmptyCallback) error {
	node, err := k.findNodeByName(name)

	if err != nil {
		return err
	}

	k.createWatcher(node, terminateTime, callback)

	return nil
}

//WatchNodeByAddress creates a watcher based on the node address
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

//MarkUnschedulable marks the given node as Unschedulable
func (k *KubeWorker) MarkUnschedulable(node *api.Node) {
	node.Spec.Unschedulable = true
	k.client.Nodes().Update(node)
}
