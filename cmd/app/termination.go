package app

import (
	"github.com/golang/glog"
	"github.com/jmccarty3/nodeSeppuku/pkg/aws"
	"github.com/jmccarty3/nodeSeppuku/pkg/k8s"
	"k8s.io/client-go/1.5/pkg/api/v1"
)

type terminateMessage struct {
	worker *k8s.KubeWorker
	node   *v1.Node
}

//Terminator responsible for terminaitng nodes
type Terminator struct {
	burstLimit     int
	terminateQueue chan terminateMessage
	removeNode     bool
}

//NewTerminator creates a new object responsible for terminating nodes
func NewTerminator(burst int, terminate bool) *Terminator {
	if burst < 0 {
		panic("Burst value must be >0")
	}
	t := &Terminator{
		burstLimit:     burst,
		terminateQueue: make(chan terminateMessage),
		removeNode:     terminate,
	}

	for i := 0; i < burst; i++ {
		go t.terminateWorker()
	}
	return t
}

//NodeEmpty callback function to be called by the k8s worker
func (t *Terminator) NodeEmpty(w *k8s.KubeWorker, node *v1.Node) {
	if !t.removeNode {
		glog.Warning("Termination prevented.")
		return
	}
	if t.burstLimit > 0 {
		t.terminateQueue <- terminateMessage{
			worker: w,
			node:   node,
		}
	} else {
		go terminate(w, node)
	}
}

func (t *Terminator) terminateWorker() {
	for m := range t.terminateQueue {
		terminate(m.worker, m.node)
	}
}

func terminate(w *k8s.KubeWorker, node *v1.Node) {
	empty, err := w.VerifyNodeEmpty(node)
	if err != nil {
		glog.Errorf("Could not verify node: %s is empty. Error: %v", node.GetName(), err)
		return
	}
	if !empty {
		glog.Warning("Node %s no longer empty. Skipping")
		return
	}

	w.MarkUnschedulable(node)
	worker := aws.NewAWSWorkerFromNode(node)
	if err = worker.RemoveNode(); err != nil {
		glog.Errorf("Could not remove node from AWS: %v", err)
		// Timer will become eligable for termination again next cycle
	}

}
