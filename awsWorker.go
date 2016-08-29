package main

import (
	"errors"
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
)

type AWSWorker struct {
	client     *autoscaling.AutoScaling
	instanceID string
	region     string
	address    string
}

const (
	InstnaceIDParam = "instance-id"
	RegionParam     = "Region"
	DefaultRegion   = "us-east-1"
)

func getRegion(config map[string]string) string {
	if r, exists := config[RegionParam]; exists && r != "" {
		return r
	}

	if region, err := getRegionFromMetadata(); err == nil {
		return region
	}

	return DefaultRegion
}

func getRegionFromMetadata() (string, error) {
	//Try the metadata service
	svc := ec2metadata.New(session.New(&aws.Config{}))
	var err error
	var region string

	if region, err = svc.Region(); err == nil {
		return region, nil
	}

	return "", fmt.Errorf("Unable to get region from metadata. %v", err)
}

func getInstanceIDFromMetadata() (string, error) {
	//Try the metadata service
	svc := ec2metadata.New(session.New(&aws.Config{}))
	var id string
	var err error

	if id, err = svc.GetMetadata("instance-id"); err == nil {
		return id, nil
	}

	glog.Warning("Error getting instance-id from metadata", err)
	return "", errors.New("Unable to get instance id")
}

func getInstanceID(config map[string]string) (string, error) {
	if id, exists := config[InstnaceIDParam]; exists && id != "" {
		return id, nil
	}

	return getInstanceIDFromMetadata()
}

func getPrivateAddress() (string, error) {
	svc := ec2metadata.New(session.New(&aws.Config{}))
	var address string
	var err error

	if address, err = svc.GetMetadata("local-ipv4"); err == nil {
		return address, nil
	}

	glog.Error("Could not get private ip from metadata. ", err)
	return "", errors.New("Unable to get Address")
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

func (w *AWSWorker) GetAddress() string {
	return w.address
}

func NewAWSWorker(config map[string]string) *AWSWorker {
	w := &AWSWorker{
		client: autoscaling.New(session.New(&aws.Config{
			Credentials: getCreds(),
			Region:      aws.String(getRegion(config)),
		})),
	}

	w.region = *w.client.Config.Region

	var err error
	if w.instanceID, err = getInstanceID(config); err != nil {
		panic("Can't get an instance id")
	}

	if w.address, err = getPrivateAddress(); err != nil {
		panic(fmt.Sprintf("Cannot obtain address. %v", err))
	}

	glog.Infof("AWS Worker running for instance: %s in region: %s", w.instanceID, w.region)

	return w
}
