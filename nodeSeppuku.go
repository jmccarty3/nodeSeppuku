package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/golang/glog"

	"k8s.io/kubernetes/pkg/api"
)

var (
	argAPIURL        = flag.String(APIURLParam, "", "Api Server url")
	argInstanceID    = flag.String(InstnaceIDParam, "", "AWS Instance ID of kubelet to watch")
	argKubeletName   = flag.String("kubelet-name", "", "Kubelet Name to search for")
	argKubeletIP     = flag.String("kubelet-address", "", "Kubelet node address to search for")
	argTerminateTime = flag.Int64("termination-time", 10, "How long (in minutes) a Node must have no pods before being terminated")
	argSelfTest      = flag.Bool("self-test", false, "Perform simple self test")
	argKubeletHost   = flag.String("kubelet-host", "localhost", "Hostname where the kubelet server exists")
	argKubeletPort   = flag.String("kubelet-port", "10255", "Port address of the readonly port")
)

//ConfigInfo is used to pass configuration information to objects
type ConfigInfo map[string]string

func main() {
	flag.Parse()

	if *argSelfTest {
		fmt.Print("All good")
		return
	}
	config := make(map[string]string)

	config[APIURLParam] = *argAPIURL

	config[InstnaceIDParam] = *argInstanceID

	aw := NewAWSWorker(config)
	kube := newKubeWorker(config)
	termTime := time.Duration(*argTerminateTime) * time.Minute

	deathFunc := func(node *api.Node) {
		glog.Info("Nodes Empty!")
		kube.MarkUnschedulable(node)

		if err := aw.RemoveNode(); err != nil {
			glog.Fatalf("Could not remove node: %v", err)
		}
	}

	var err error
	if *argKubeletName == "" && *argKubeletIP == "" {
		*argKubeletIP = aw.GetAddress()
	}

	if *argKubeletName != "" {
		err = kube.WatchNodeByName(*argKubeletName, &termTime, deathFunc)
	} else {
		err = kube.WatchNodeByAddress(*argKubeletIP, &termTime, deathFunc)
	}

	if err != nil {
		panic(fmt.Sprint("Error watching Kube node: ", err))
	}

	select {}
}
