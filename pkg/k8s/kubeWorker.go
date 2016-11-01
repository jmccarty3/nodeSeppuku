package k8s

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang/glog"

	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/pkg/fields"
	"k8s.io/client-go/1.5/pkg/labels"
	"k8s.io/client-go/1.5/pkg/runtime"
	"k8s.io/client-go/1.5/pkg/util/wait"
	"k8s.io/client-go/1.5/tools/cache"
	"k8s.io/client-go/1.5/tools/clientcmd"
)

type podWatcher struct {
	name       string
	controller *cache.Controller
	store      cache.StoreToPodLister
	killTimer  *killTimer
	stopChan   chan struct{}
}

//KubeWorker operates on a kubernetes cluster
type KubeWorker struct {
	client         *kubernetes.Clientset
	nodeController *cache.Controller
	scheme         *runtime.Scheme
	nodeIndex      map[string]*podWatcher
	indexLock      sync.Mutex
	terminateTime  *time.Duration
	emptyCallback  NodeEmptyCallback
}

//NodeEmptyCallback represents the function to call when then node is empty
type NodeEmptyCallback func(worker *KubeWorker, node *v1.Node)

//NewKubeWorker creates a new worker for a kubernetes cluster
func NewKubeWorker(masterURL, kubeConfig string, terminateTime *time.Duration, callback NodeEmptyCallback) *KubeWorker {
	config, err := clientcmd.BuildConfigFromFlags(masterURL, kubeConfig)
	if err != nil {
		glog.Exit(err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Exit(err)
	}

	s := runtime.NewScheme()
	v1.RegisterConversions(s)

	return &KubeWorker{
		client:        clientset,
		scheme:        s,
		nodeIndex:     make(map[string]*podWatcher),
		terminateTime: terminateTime,
		emptyCallback: callback,
	}
}

func (k *KubeWorker) findNodeByName(name string) (*v1.Node, error) {
	return k.client.Nodes().Get(name)
}

func (k *KubeWorker) findNodeByAddress(address string) (v1.Node, error) {
	nodes, err := k.client.Nodes().List(api.ListOptions{
		LabelSelector: labels.Everything(),
		FieldSelector: fields.Everything(),
	})

	if err != nil {
		return v1.Node{}, err
	}

	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			if a.Address == address {
				return n, nil
			}
		}
	}

	return v1.Node{}, errors.New("Unable to find node by address")
}

func (k *KubeWorker) addNodeToWatch(obj interface{}) {
	node, ok := obj.(*v1.Node)
	if !ok {
		glog.Errorf("add node cannot convert to *v1.Node: %v", obj)
		return
	}

	k.addNodeToIndex(node, k.createWatcher(node))
}

func (k *KubeWorker) removeNodeFromWatch(obj interface{}) {
	var node *v1.Node
	switch t := obj.(type) {
	case *v1.Node:
		node = t
	case cache.DeletedFinalStateUnknown: //https://godoc.org/k8s.io/kubernetes/pkg/client/cache#DeletedFinalStateUnknown
		var ok bool
		node, ok = t.Obj.(*v1.Node)
		if !ok {
			glog.Errorf("remove node cannot convert to *v1.Node: %v", t.Obj)
			return
		}
	default:
		glog.Errorf("remove node cannot convert to *api.Node: %v", t)
		return
	}

	k.removeNodeFromIndex(node)
	glog.V(4).Infof("Remove node %s", node.Name)
}

func (k *KubeWorker) addNodeToIndex(node *v1.Node, watcher *podWatcher) error {
	k.indexLock.Lock()
	defer k.indexLock.Unlock()
	if _, ok := k.nodeIndex[node.GetName()]; ok {
		glog.V(4).Infof("Node %v already exists in the node index", node.GetName())
		return nil
	}
	k.nodeIndex[node.GetName()] = watcher
	glog.V(4).Infof("Added node %s to index", node.GetName())
	return nil //TODO: Return error
}

func (k *KubeWorker) removeNodeFromIndex(node *v1.Node) *podWatcher {
	k.indexLock.Lock()
	defer k.indexLock.Unlock()

	removed := k.nodeIndex[node.GetName()]
	delete(k.nodeIndex, node.GetName())
	removed.stopChan <- struct{}{}
	removed.killTimer.StopIfRunning()
	return removed
}

func (k *KubeWorker) createNodeWatcher() {
	lw := cache.NewListWatchFromClient(k.client.Core().GetRESTClient(), "nodes", api.NamespaceAll, fields.Everything())

	_, k.nodeController = cache.NewInformer(
		lw,
		&v1.Node{},
		0,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    k.addNodeToWatch,
			DeleteFunc: k.removeNodeFromWatch,
		},
	)

	go k.nodeController.Run(wait.NeverStop)
	//Wait for initial sync
	for k.nodeController.HasSynced() == false {
		time.Sleep(1 * time.Second)
	}
	glog.Info("Initial node sync complete")
}

func (k *KubeWorker) createWatcher(node *v1.Node) *podWatcher {
	f := fields.Set{
		"spec.nodeName": node.Name}
	lw := cache.NewListWatchFromClient(k.client.Core().GetRESTClient(), "pods", api.NamespaceAll, fields.SelectorFromSet(f))
	watcher := &podWatcher{
		name:     node.GetName(),
		stopChan: make(chan struct{}),
	}

	watcher.store.Indexer, watcher.controller = cache.NewIndexerInformer(
		lw,
		&v1.Pod{},
		0,
		cache.ResourceEventHandlerFuncs{},
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)

	//TODO: Remove this. It should probably only be done when added to the index
	go watcher.controller.Run(watcher.stopChan)
	//Wait for initial sync
	for watcher.controller.HasSynced() == false {
		time.Sleep(1 * time.Second)
	}
	glog.V(3).Infof("Initial pod sync for node %s complete", node.GetName())

	watcher.killTimer = newKillTimer(node.GetName())
	go func() {
		<-watcher.killTimer.C

		k.emptyCallback(k, node)
	}()
	return watcher
}

//WatchNodeByName creates a watcher based on nodename given
func (k *KubeWorker) WatchNodeByName(name string) error {
	node, err := k.findNodeByName(name)

	if err != nil {
		return err
	}

	k.addNodeToIndex(node, k.createWatcher(node))
	return nil
}

//WatchNodeByAddress creates a watcher based on the node address
func (k *KubeWorker) WatchNodeByAddress(address string) error {
	node, err := k.findNodeByAddress(address)

	if err != nil {
		return err
	}
	glog.Info(node.Name)
	glog.Info(k.client.ServerVersion())

	k.addNodeToIndex(&node, k.createWatcher(&node))
	return nil
}

//WatchAllNodes sets the worker to monitor all node changes in the cluster
func (k *KubeWorker) WatchAllNodes() {
	glog.Info("Preparing to watch all nodes in system")
	k.createNodeWatcher()
	glog.Info("Setup complete")
}

//MarkUnschedulable marks the given node as Unschedulable
func (k *KubeWorker) MarkUnschedulable(node *v1.Node) {
	//Getting the most up to date node
	n, _ := k.client.Nodes().Get(node.GetName())
	n.Spec.Unschedulable = true
	if _, err := k.client.Nodes().Update(n); err != nil {
		glog.Errorf("Error marking node Unschedulable: %v", err)
	}
}

//VerifyNodeEmpty allows a client to verify a node is still empty before removal
func (k *KubeWorker) VerifyNodeEmpty(node *v1.Node) (bool, error) {
	k.indexLock.Lock()
	defer k.indexLock.Unlock()

	var w *podWatcher
	var ok bool
	if w, ok = k.nodeIndex[node.GetName()]; !ok {
		return false, fmt.Errorf("Node %s not found", node.GetName())
	}
	return isNodeEmpty(w.store), nil
}

//Run exececutes the primary control loop for the worker
func (k *KubeWorker) Run() {
	ticker := time.NewTicker(time.Minute)
	for t := range ticker.C {
		glog.V(3).Info("Checking pods at ", t)
		k.indexLock.Lock()
		for _, watcher := range k.nodeIndex {
			if isNodeEmpty(watcher.store) {
				watcher.killTimer.ResetIfNotRunning(*k.terminateTime)
			} else {
				watcher.killTimer.StopIfRunning()
			}
		}
		glog.V(3).Infof("Finished checking pods of %d nodes", len(k.nodeIndex))
		k.indexLock.Unlock()
	}
}
