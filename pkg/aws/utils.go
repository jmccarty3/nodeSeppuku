package aws

import (
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/golang/glog"
)

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

func getInstanceID(id string) (string, error) {
	if id != "" {
		return id, nil
	}

	return getInstanceIDFromMetadata()
}

func getPrivateAddressFromMetadata() (string, error) {
	svc := ec2metadata.New(session.New(&aws.Config{}))
	var address string
	var err error

	if address, err = svc.GetMetadata("local-ipv4"); err == nil {
		return address, nil
	}

	glog.Error("Could not get private ip from metadata. ", err)
	return "", errors.New("Unable to get Address")
}
