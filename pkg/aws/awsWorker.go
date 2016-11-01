package aws

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/glog"
	k8sLabels "k8s.io/client-go/1.5/pkg/api/unversioned"
	"k8s.io/client-go/1.5/pkg/api/v1"
)

//AWSWorker operates on aws resources such as instances and autoscaling groups
type AWSWorker struct {
	client     *autoscaling.AutoScaling
	instanceID string
	region     string
	address    string
}

func (w *AWSWorker) getAutoScalingGroupFromAPI() (string, error) {
	//Try to find it based on instance ID
	params := &autoscaling.DescribeAutoScalingInstancesInput{
		InstanceIds: []*string{aws.String(w.instanceID)},
		MaxRecords:  aws.Int64(1),
	}

	if resp, err := w.client.DescribeAutoScalingInstances(params); err == nil {
		if len(resp.AutoScalingInstances) > 0 {
			return *resp.AutoScalingInstances[0].AutoScalingGroupName, nil
		} else {
			glog.Warning("Instance: ", w.instanceID, " does not appear to be in an AS group")
		}
	} else {
		glog.Error("Error getting Autoscaling group by instance id. ", err)
		return "", err
	}

	return "", nil
}

func (w *AWSWorker) canDecrimentGroup(autoscalingID string) (bool, error) {
	params := &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []*string{aws.String(autoscalingID)},
	}

	resp, err := w.client.DescribeAutoScalingGroups(params)

	if err != nil {
		glog.Errorf("Could not obtain autoscaling group informaiton: %v", err)
		return false, err
	}

	return *resp.AutoScalingGroups[0].DesiredCapacity != 0, nil
}

func (w *AWSWorker) detachAndScaleASG(autoscalingID string) error {
	var decriment bool
	{
		var err error
		if decriment, err = w.canDecrimentGroup(autoscalingID); err != nil {
			glog.Warning("Cannot detach from ASG.")
			return err //Pass the error up
		}
	}

	glog.Infof("ASG %s will be decrimented: %v", autoscalingID, decriment)

	params := &autoscaling.DetachInstancesInput{
		InstanceIds:                    []*string{aws.String(w.instanceID)},
		AutoScalingGroupName:           aws.String(autoscalingID),
		ShouldDecrementDesiredCapacity: aws.Bool(decriment),
	}

	resp, err := w.client.DetachInstances(params)

	if err != nil {
		glog.Error("Failure to detach instance. Error:", err)
		return err
	}

	activtyParams := &autoscaling.DescribeScalingActivitiesInput{
		ActivityIds:          []*string{resp.Activities[0].ActivityId},
		AutoScalingGroupName: aws.String(autoscalingID),
		MaxRecords:           aws.Int64(1),
	}

	for {
		r, e := w.client.DescribeScalingActivities(activtyParams)

		if e != nil {
			glog.Error("Error waiting on activity:", e)
			return e
		}

		if *r.Activities[0].Progress >= 100 {
			glog.Info("Completed detach")
			break
		}

		time.Sleep(10 * time.Second)
	}

	return nil
}

//RemoveNode works to remove a node/instance from the system
func (w *AWSWorker) RemoveNode() error {
	if autoscalingID, err := w.getAutoScalingGroupFromAPI(); err == nil {
		if autoscalingID != "" {
			if err = w.detachAndScaleASG(autoscalingID); err != nil {
				return err
			}
		}
	} else {
		glog.Error("Could not get Autoscaling ID. Error: ", err)
		return err
	}

	//Terminate instance
	ec := ec2.New(session.New(&w.client.Config))

	terminateParams := &ec2.TerminateInstancesInput{
		InstanceIds: []*string{aws.String(w.instanceID)},
	}
	glog.Info("Calling Terminate for Instance: ", w.instanceID)
	if _, e := ec.TerminateInstances(terminateParams); e != nil {
		glog.Error("Error Terminating Instnace:", e)
		return e
	}

	return nil
}

func getCreds() *credentials.Credentials {
	return credentials.NewChainCredentials(
		[]credentials.Provider{
			&credentials.EnvProvider{},
			&ec2rolecreds.EC2RoleProvider{
				Client: ec2metadata.New(session.New(&aws.Config{})),
			},
			&credentials.SharedCredentialsProvider{},
		})
}

//GetAddress returns the IP address of the instance
func (w *AWSWorker) GetAddress() string {
	return w.address
}

func newDefaultWorker(instance, region, address string) *AWSWorker {
	worker := &AWSWorker{
		client: autoscaling.New(session.New(&aws.Config{
			Credentials: getCreds(),
			Region:      aws.String(region),
		})),
	}
	worker.region = *worker.client.Config.Region
	worker.instanceID = instance
	worker.address = address
	glog.Infof("AWS Worker created for instance: %s in region: %s", instance, region)
	return worker
}

//NewAWSWorkerFromNode creates an AWS Worker based on the node information
// expects the cloud Provider information turned on
func NewAWSWorkerFromNode(node *v1.Node) *AWSWorker {
	region := node.Labels[k8sLabels.LabelZoneRegion]
	instanceID := node.Spec.ExternalID
	if region == "" || instanceID == "" {
		panic(fmt.Sprintf("Missing cloud provider information for node %s", node.Name))
	}
	var address string

	for _, a := range node.Status.Addresses {
		if a.Type == v1.NodeInternalIP {
			address = a.Address
			break
		}
	}

	return newDefaultWorker(instanceID, region, address)
}

//NewAWSWorkerFromMetadata creates a new AWSWorker from metadata
func NewAWSWorkerFromMetadata() *AWSWorker {
	instanceID, err := getInstanceIDFromMetadata()
	if err != nil {
		panic("Can't get an instance id from metadata")
	}

	region, err := getRegionFromMetadata()
	if err != nil {
		panic("Can't get region from metadata")
	}

	address, err := getPrivateAddressFromMetadata()
	if err != nil {
		panic(fmt.Sprintf("Cannot obtain address. %v", err))
	}
	return newDefaultWorker(instanceID, region, address)
}
