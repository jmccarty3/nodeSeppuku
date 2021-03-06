package k8s

import (
	"regexp"
	"sync"
	"time"

	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/pkg/runtime"

	"github.com/golang/glog"
)

type killTimer struct {
	name     string
	timer    *time.Timer
	timerSet bool
	lock     sync.Mutex
	C        <-chan time.Time
	done     chan struct{}
	callback func()
}

func newKillTimer(name string, callback func()) *killTimer {
	timer := &killTimer{
		name:     name,
		timer:    time.NewTimer(10 * time.Minute),
		timerSet: false, //We will stop it immedially following this
		done:     make(chan struct{}, 1),
		callback: callback,
	}
	timer.timer.Stop()
	timer.C = timer.timer.C
	return timer
}

func (k *killTimer) tick() {

	select {
	case <-k.C:
		// Not locking the timer here to prevent a deadlock in downsteam code.
		// It is possible for the callback to fire right as someone is turning of the timer.
		// Downstream code should handle this.
		glog.V(2).Infof("Timer %s up \n", k.name)
		k.callback()
		k.lock.Lock()
		k.timerSet = false
		k.lock.Unlock()
	case <-k.done:
	}
	return
}

//StopIfRunning stops the wrapped timer if it is currently set
func (k *killTimer) StopIfRunning() {
	k.lock.Lock()
	defer k.lock.Unlock()

	if k.timerSet {
		glog.Infof("Timer %s canceling", k.name)
		k.timer.Stop()
		k.done <- struct{}{}
		k.timerSet = false
	}
}

//ResetIfNotRunning restes the wrapper timer if it is not already running
func (k *killTimer) ResetIfNotRunning(duration time.Duration) {
	k.lock.Lock()
	defer k.lock.Unlock()

	if !k.timerSet {
		glog.Infof("Timer %s setting kill timer for %v \n", k.name, duration)
		go k.tick()
		k.timer.Reset(duration)
		k.timerSet = true
	}

}

func isPodDaemonset(pod *v1.Pod) bool {
	//Same process as kubectl drain
	creatorRef, found := pod.ObjectMeta.Annotations[api.CreatedByAnnotation]

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

func isNodeEmpty(pods []interface{}) bool {
	re := regexp.MustCompile("gcr.io/google_containers/pause")
	//Currently using the Indexer List since the listwatcher is returning *v1.Pod but the StoreToPodLister is hard coded to expect *api.Pod
	for _, i := range pods {
		p := i.(*v1.Pod)
		if p.Status.Phase == v1.PodRunning || p.Status.Phase == v1.PodPending {
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

			glog.V(4).Info("Pod Alive:", p.Name)
			return false
		}
	}
	glog.V(4).Info("Pods Empty.")
	return true
}
