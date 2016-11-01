package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/jmccarty3/nodeSeppuku/cmd/app"
	"github.com/jmccarty3/nodeSeppuku/pkg/k8s"
)

// Determine if running as controller or standalone

// If standalone create worker for this node

// If controller, iterate over all nodes, creating worker for node (add flag to ignore non-created workers)

// Standalone, work as normal

// Controller
//  Iterate over all nodes
//    If not in catalog, creste new entry
//    IF removed from node list, remove catalog entry - Utilize informers?

var (
	argMasterURL          = flag.String("master-url", "", "API Server url")
	argKubeConfig         = flag.String("kube-config", "", "Kube config location")
	argTerminateTime      = flag.Int("termination-time", 10, "How long in minutes a node must remain empty")
	argSelfTest           = flag.Bool("self-test", false, "Perform simple self test")
	argPreventTermination = flag.Bool("disable-termination", false, "Run without termination enabled. For debugging.")
	argBurstLimit         = flag.Int("burst-limit", 0, "Burst limit for node termination. 0 indicates no limit.")
)

func main() {
	flag.Parse()

	if *argSelfTest {
		fmt.Print("Self Test")
		return
	}

	t := app.NewTerminator(*argBurstLimit, !*argPreventTermination)
	termTime := time.Duration(*argTerminateTime) * time.Minute
	kw := k8s.NewKubeWorker(*argMasterURL, *argKubeConfig, &termTime, t.NodeEmpty)

	kw.WatchAllNodes()
	kw.Run()

}
