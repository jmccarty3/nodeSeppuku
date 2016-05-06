package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/api"
)

func panicError(err error) {
	if err != nil {
		panic(err)
	}
}

func getNameFromKubeletOrDie(host, port string) string {
	glog.Warningf("Attempt to extract node name from pods on host: %s port: %s", host, port)
	url := fmt.Sprintf("http://%s:%s/pods", host, port)

	res, err := http.Get(url)
	panicError(err)

	defer res.Body.Close()

	var pods api.PodList
	decoder := json.NewDecoder(res.Body)
	err = decoder.Decode(&pods)

	panicError(err)

	name, err := getNameFromPodList(&pods)
	panicError(err)

	glog.Infof("Extracted node name: %s", name)

	return name
}

func getNameFromPodList(pods *api.PodList) (string, error) {
	if len(pods.Items) == 0 {
		return "", errors.New("Empty Pod List")
	}

	return pods.Items[0].Spec.NodeName, nil
}
