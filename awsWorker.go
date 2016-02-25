package main

import (
	"errors"
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
	client        *autoscaling.AutoScaling
	instanceID    string
	autoscalingID string
	region        string
}

const (
	InstnaceIDParam = "instance-id"
	ASIDPram        = "autoscaling"
	RegionParam     = "Region"
	DefaultRegion   = "us-east-1"
)

func getRegion(config map[string]string) string {
	if r, exists := config[RegionParam]; exists && r != "" {
		return r
	}

	return DefaultRegion
}

func getInstnceID(config map[string]string) (string, error) {
	if id, exists := config[InstnaceIDParam]; exists && id != "" {
		return id, nil
	}

	//Try the metadata service
	svc := ec2metadata.New(session.New(&aws.Config{}))

	if id, err := svc.GetMetadata("instance-id"); err == nil {
		return id, nil
	} else {
		glog.Warning("Error getting instance-id from metadata", err)
	}

	return "", errors.New("Unable to get instance id")
}

func (w *AWSWorker) getAutoScalingGroup(config map[string]string) (string, error) {
	if as, exists := config[ASIDPram]; exists && as != "" {
		return as, nil
	}

	//Try to find it based on instance ID
	params := &autoscaling.DescribeAutoScalingInstancesInput{
		InstanceIds: []*string{aws.String(w.instanceID)},
		MaxRecords:  aws.Int64(1),
	}

	if resp, err := w.client.DescribeAutoScalingInstances(params); err == nil && len(resp.AutoScalingInstances) > 0 {
		return *resp.AutoScalingInstances[0].AutoScalingGroupName, nil
	} else {
		glog.Warning("Error getting Autoscaling group by instance id. ", err)
	}

	return "", errors.New("Unable to get aws autoscaling group for instance")
}

func (w *AWSWorker) RemoveNode() error {
	params := &autoscaling.DetachInstancesInput{
		InstanceIds:                    []*string{aws.String(w.instanceID)},
		AutoScalingGroupName:           aws.String(w.autoscalingID),
		ShouldDecrementDesiredCapacity: aws.Bool(true),
	}

	resp, err := w.client.DetachInstances(params)

	if err != nil {
		glog.Error("Failure to detach instance. Error:", err)
		return err
	}

	activtyParams := &autoscaling.DescribeScalingActivitiesInput{
		ActivityIds:          []*string{resp.Activities[0].ActivityId},
		AutoScalingGroupName: aws.String(w.autoscalingID),
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

	//Terminate instance
	ec := ec2.New(session.New(&w.client.Config))

	terminateParams := &ec2.TerminateInstancesInput{
		InstanceIds: []*string{aws.String(w.instanceID)},
	}

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

func NewAWSWorker(config map[string]string) *AWSWorker {
	w := &AWSWorker{
		client: autoscaling.New(session.New(&aws.Config{
			Region: aws.String(getRegion(config)),
		})),
	}

	var err error
	if w.instanceID, err = getInstnceID(config); err != nil {
		panic("Can't get an instance id")
	}

	if w.autoscalingID, err = w.getAutoScalingGroup(config); err != nil {
		panic("Can't get an autoscaling id")
	}

	glog.Infof("AWS Worker running for instance: %s in AutoscalingGroup: %s", w.instanceID, w.autoscalingID)

	return w
}
