package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/golang/glog"

	"k8s.io/kubernetes/pkg/api"
)

var (
	argAPIHost       = flag.String(APIHostParam, "localhost", "Api Server host name")
	argAPIPort       = flag.String(APIPortParam, "8080", "Api Server Port")
	argInstanceID    = flag.String(InstnaceIDParam, "", "AWS Instance ID of kubelet to watch")
	argKubeletName   = flag.String("kubelet-name", "", "Kubelet Name to search for")
	argKubeletIP     = flag.String("kubelet-address", "", "Kubelet node address to search for")
	argTerminateTime = flag.Int64("termination-time", 10, "How long (in minutes) a Node must have no pods before being terminated")
	argSelfTest      = flag.Bool("self-test", false, "Perform simple self test")
)

type ConfigInfo map[string]string

func main() {
	flag.Parse()

	if *argSelfTest {
		fmt.Print("All good")
		return
	}
	config := make(map[string]string)

	config[APIHostParam] = *argAPIHost
	config[APIPortParam] = *argAPIPort

	config[InstnaceIDParam] = *argInstanceID

	if *argKubeletIP == "" && *argKubeletName == "" {
		glog.Fatal("Cannot pass both an empty kubelet name and IP. Pick one")
	}

	aw := NewAWSWorker(config)

	kube := NewKubeWorker(config)
	termTime := time.Duration(*argTerminateTime) * time.Minute

	deathFunc := func(node *api.Node) {
		fmt.Print("Nodes Empty!")
		kube.MarkUnschedulable(node)
		fmt.Println("Remove Result:", aw.RemoveNode())
	}

	var err error
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
